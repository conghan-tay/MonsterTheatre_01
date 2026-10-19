// Package anthropic is the language-model adapter backed by Claude, through
// the official Anthropic Go SDK.
//
// Text from people, the director's scene and characters and the lines the
// audience chose, is placed inside tags and escaped so it cannot close them;
// the system prompts say it is story material, never instructions (D058).
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// DefaultModel is used when LLM_MODEL is not set.
const DefaultModel = "claude-sonnet-5-5"

// maxTokens bounds one response, including the model's own reasoning. The
// answers are a few sentences; the room is so that a response is never cut
// off mid-thought.
const maxTokens = 16000

// Model is a providers.DialogueModel that asks Claude.
type Model struct {
	// Caching marks the fixed start of the choices request for Claude's
	// prompt cache (docs/decisions.md, D057). New turns it on.
	Caching bool
	// Logger receives one line per call: its tokens, its cost, and the
	// room's running totals. New gives one that discards.
	Logger *slog.Logger
	totals roomTotals

	client sdk.Client
	model  string
	// choicesPrompt is the choices system prompt with the line length
	// filled in.
	choicesPrompt string
}

var _ providers.DialogueModel = (*Model)(nil)

// New returns the adapter. model is the Claude model ID; empty means
// DefaultModel. lineLength is how long a line the model is asked to write,
// in characters (providers.LineLimit works it out from the configured
// limit). Extra options are for tests, which point it at a local server.
//
// The SDK's own retries are turned off: every call here runs inside a
// Temporal activity, and the workflow decides how often and how long to
// retry (docs/system-design.md, section 4).
func New(apiKey, model string, lineLength int, opts ...option.RequestOption) *Model {
	if model == "" {
		model = DefaultModel
	}
	opts = append([]option.RequestOption{option.WithAPIKey(apiKey), option.WithMaxRetries(0)}, opts...)
	return &Model{
		Caching:       true,
		Logger:        slog.New(slog.DiscardHandler),
		client:        sdk.NewClient(opts...),
		model:         model,
		choicesPrompt: fmt.Sprintf(choicesSystemPrompt, lineLength),
	}
}

// choicesSchema makes the answer three named strings, so that the count is
// part of the format and not something the model has to remember.
var choicesSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"line1": map[string]any{"type": "string", "description": "The first candidate line."},
		"line2": map[string]any{"type": "string", "description": "The second candidate line, taking the scene a different way."},
		"line3": map[string]any{"type": "string", "description": "The third candidate line, different again."},
	},
	"required":             []string{"line1", "line2", "line3"},
	"additionalProperties": false,
}

var taggedSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"tagged": map[string]any{"type": "string", "description": "The line with audio tags inserted and its words unchanged."},
	},
	"required":             []string{"tagged"},
	"additionalProperties": false,
}

// GenerateChoices asks for a turn's three candidate lines.
func (m *Model) GenerateChoices(ctx context.Context, req providers.ChoicesRequest) (providers.Choices, error) {
	var answer struct {
		Line1 string `json:"line1"`
		Line2 string `json:"line2"`
		Line3 string `json:"line3"`
	}
	model, err := m.ask(ctx, call{kind: "choices", room: req.RoomID, turn: req.Turn, attempt: req.Attempt},
		m.choicesPrompt, []string{roomContext(req), turnMessage(req)}, m.Caching, choicesSchema, &answer)
	if err != nil {
		return providers.Choices{}, fmt.Errorf("generate choices: %w", err)
	}
	return providers.Choices{Lines: []string{answer.Line1, answer.Line2, answer.Line3}, Model: model}, nil
}

// EnhanceTags asks for audio tags on a winning line.
func (m *Model) EnhanceTags(ctx context.Context, req providers.TagsRequest) (providers.Tagged, error) {
	var answer struct {
		Tagged string `json:"tagged"`
	}
	// Never cached: the request is below the model's minimum cacheable
	// length, so a marker would do nothing.
	model, err := m.ask(ctx, call{kind: "tags", room: req.RoomID, turn: req.Turn, attempt: req.Attempt},
		tagsSystemPrompt, []string{tagsMessage(req)}, false, taggedSchema, &answer)
	if err != nil {
		return providers.Tagged{}, fmt.Errorf("enhance tags: %w", err)
	}
	return providers.Tagged{Text: answer.Tagged, Model: model}, nil
}

// call says which request a call is, for its log line.
type call struct {
	kind          string
	room          string
	turn, attempt int
}

// ask sends one request and decodes the structured answer into out. It
// returns the ID of the model that answered. user is the user message's
// text blocks, in order.
//
// With cache set, the system prompt and the first user block are marked for
// the prompt cache: they are what stays the same from one request to the
// next. Without it the same text is sent unmarked.
//
// Effort is low: these are short creative tasks, and the audience is
// waiting while they run.
func (m *Model) ask(ctx context.Context, c call, system string, user []string, cache bool, schema map[string]any, out any) (string, error) {
	systemBlock := sdk.TextBlockParam{Text: system}
	blocks := make([]sdk.ContentBlockParamUnion, len(user))
	for i, text := range user {
		blocks[i] = sdk.ContentBlockParamUnion{OfText: &sdk.TextBlockParam{Text: text}}
	}
	if cache {
		systemBlock.CacheControl = sdk.NewCacheControlEphemeralParam()
		blocks[0].OfText.CacheControl = sdk.NewCacheControlEphemeralParam()
	}
	resp, err := m.client.Messages.New(ctx, sdk.MessageNewParams{
		Model:     m.model,
		MaxTokens: maxTokens,
		System:    []sdk.TextBlockParam{systemBlock},
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(blocks...)},
		OutputConfig: sdk.OutputConfigParam{
			Effort: sdk.OutputConfigEffortLow,
			Format: sdk.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) && providers.PermanentStatus(apiErr.StatusCode) {
			// A wrong key, an unknown model, a rejected request: the same
			// request would be refused again.
			return "", providers.Permanent(err)
		}
		return "", err
	}

	// Logged before the answer is judged: a refused, cut-off, or unusable
	// answer was paid for all the same.
	m.logUsage(c, string(resp.Model), resp.Usage)

	// Checked before the content is read: a refusal or a cut-off answer has
	// content, but not an answer. Both count as a failed attempt.
	switch resp.StopReason {
	case sdk.StopReasonRefusal:
		return "", errors.New("the model declined to answer")
	case sdk.StopReasonMaxTokens:
		return "", errors.New("the answer was cut off at the token limit")
	}

	var text strings.Builder
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(sdk.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return "", fmt.Errorf("the answer is not the JSON asked for: %w", err)
	}
	return string(resp.Model), nil
}

// logUsage writes one call's tokens and cost, with its room's running
// totals.
func (m *Model) logUsage(c call, model string, usage sdk.Usage) {
	cost, withoutCache, priced := price(model, usage.InputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens, usage.OutputTokens)
	total := m.totals.add(c.room, cost, withoutCache)
	dollars := func(v float64) any {
		if !priced {
			return "unpriced"
		}
		return v
	}
	m.Logger.Info("claude call",
		"kind", c.kind, "room_id", c.room, "turn", c.turn, "attempt", c.attempt,
		"model", model, "caching", m.Caching,
		"input_tokens", usage.InputTokens,
		"cache_write_tokens", usage.CacheCreationInputTokens,
		"cache_read_tokens", usage.CacheReadInputTokens,
		"output_tokens", usage.OutputTokens,
		"cost_usd", dollars(cost), "cost_without_cache_usd", dollars(withoutCache),
		"room_calls", total.calls, "room_cost_usd", total.cost, "room_cost_without_cache_usd", total.withoutCache,
	)
}

// escape keeps text written by people, or lines they chose, from closing
// the tag it is placed in or opening one of its own: < and > become &lt;
// and &gt;. An ampersand is left alone; it cannot form a tag, and the tags
// check compares a line's words letter for letter (docs/decisions.md, D058).
var escape = strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace

// roomContext is the part of the choices request that is the same on every
// turn of a performance: the scene and both characters. It is the cached
// block (D057).
func roomContext(req providers.ChoicesRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<scene>\nTopic: %s\n", escape(req.Scene.Topic))
	if req.Scene.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", escape(req.Scene.Description))
	}
	if req.Scene.Genre != "" {
		fmt.Fprintf(&b, "Genre: %s\n", escape(req.Scene.Genre))
	}
	b.WriteString("</scene>\n\n")

	writeCharacter(&b, "A", req.CharacterA)
	writeCharacter(&b, "B", req.CharacterB)
	return b.String()
}

// turnMessage is the part of the choices request that changes every turn.
func turnMessage(req providers.ChoicesRequest) string {
	var b strings.Builder
	b.WriteString("<dialogue_so_far>\n")
	if len(req.History) == 0 {
		b.WriteString("(Nothing yet. This is the first line of the scene.)\n")
	}
	for _, line := range req.History {
		fmt.Fprintf(&b, "%s: %s\n", speakerName(line.Speaker, req), escape(line.Text))
	}
	b.WriteString("</dialogue_so_far>\n\n")

	fmt.Fprintf(&b, "This is turn %d of %d. %s speaks. The story beat is %s. ",
		req.Turn, req.Turn+req.TurnsRemaining, speakerName(req.Speaker, req), req.Beat)
	switch req.TurnsRemaining {
	case 0:
		b.WriteString("This is the final turn: it must conclude the scene.")
	case 1:
		b.WriteString("One turn remains after this one.")
	default:
		fmt.Fprintf(&b, "%d turns remain after this one.", req.TurnsRemaining)
	}
	return b.String()
}

func tagsMessage(req providers.TagsRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<character>\nName: %s\n", escape(req.Character.Name))
	if req.Character.Brief != "" {
		fmt.Fprintf(&b, "Brief: %s\n", escape(req.Character.Brief))
	}
	fmt.Fprintf(&b, "</character>\n\nStory beat: %s\n\n<line>%s</line>", escape(req.Beat), escape(req.Text))
	return b.String()
}

func writeCharacter(b *strings.Builder, slot string, c providers.Character) {
	fmt.Fprintf(b, "<character_%s>\nName: %s\n", strings.ToLower(slot), escape(c.Name))
	if c.Brief != "" {
		fmt.Fprintf(b, "Brief: %s\n", escape(c.Brief))
	}
	fmt.Fprintf(b, "</character_%s>\n\n", strings.ToLower(slot))
}

// speakerName turns a slot, "A" or "B", into the character's name, escaped
// for use inside a tag.
func speakerName(slot string, req providers.ChoicesRequest) string {
	if slot == "B" {
		return escape(req.CharacterB.Name)
	}
	return escape(req.CharacterA.Name)
}
