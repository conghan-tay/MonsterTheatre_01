// Package anthropic is the language-model adapter backed by Claude, through
// the official Anthropic Go SDK.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	client sdk.Client
	model  string
}

var _ providers.DialogueModel = (*Model)(nil)

// New returns the adapter. model is the Claude model ID; empty means
// DefaultModel. Extra options are for tests, which point it at a local
// server.
//
// The SDK's own retries are turned off: every call here runs inside a
// Temporal activity, and the workflow decides how often and how long to
// retry (docs/system-design.md, section 4).
func New(apiKey, model string, opts ...option.RequestOption) *Model {
	if model == "" {
		model = DefaultModel
	}
	opts = append([]option.RequestOption{option.WithAPIKey(apiKey), option.WithMaxRetries(0)}, opts...)
	return &Model{client: sdk.NewClient(opts...), model: model}
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
	model, err := m.ask(ctx, choicesSystemPrompt, choicesMessage(req), choicesSchema, &answer)
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
	model, err := m.ask(ctx, tagsSystemPrompt, tagsMessage(req), taggedSchema, &answer)
	if err != nil {
		return providers.Tagged{}, fmt.Errorf("enhance tags: %w", err)
	}
	return providers.Tagged{Text: answer.Tagged, Model: model}, nil
}

// ask sends one request and decodes the structured answer into out. It
// returns the ID of the model that answered.
//
// Effort is low: these are short creative tasks, and the audience is
// waiting while they run.
func (m *Model) ask(ctx context.Context, system, user string, schema map[string]any, out any) (string, error) {
	resp, err := m.client.Messages.New(ctx, sdk.MessageNewParams{
		Model:     m.model,
		MaxTokens: maxTokens,
		System:    []sdk.TextBlockParam{{Text: system}},
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(user))},
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

// choicesMessage is the part of the request that changes every turn.
func choicesMessage(req providers.ChoicesRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<scene>\nTopic: %s\n", req.Scene.Topic)
	if req.Scene.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", req.Scene.Description)
	}
	if req.Scene.Genre != "" {
		fmt.Fprintf(&b, "Genre: %s\n", req.Scene.Genre)
	}
	b.WriteString("</scene>\n\n")

	writeCharacter(&b, "A", req.CharacterA)
	writeCharacter(&b, "B", req.CharacterB)

	b.WriteString("<dialogue_so_far>\n")
	if len(req.History) == 0 {
		b.WriteString("(Nothing yet. This is the first line of the scene.)\n")
	}
	for _, line := range req.History {
		fmt.Fprintf(&b, "%s: %s\n", speakerName(line.Speaker, req), line.Text)
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
	fmt.Fprintf(&b, "<character>\nName: %s\n", req.Character.Name)
	if req.Character.Brief != "" {
		fmt.Fprintf(&b, "Brief: %s\n", req.Character.Brief)
	}
	fmt.Fprintf(&b, "</character>\n\nStory beat: %s\n\n<line>%s</line>", req.Beat, req.Text)
	return b.String()
}

func writeCharacter(b *strings.Builder, slot string, c providers.Character) {
	fmt.Fprintf(b, "<character_%s>\nName: %s\n", strings.ToLower(slot), c.Name)
	if c.Brief != "" {
		fmt.Fprintf(b, "Brief: %s\n", c.Brief)
	}
	fmt.Fprintf(b, "</character_%s>\n\n", strings.ToLower(slot))
}

// speakerName turns a slot, "A" or "B", into the character's name.
func speakerName(slot string, req providers.ChoicesRequest) string {
	if slot == "B" {
		return req.CharacterB.Name
	}
	return req.CharacterA.Name
}
