package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// api stands in for the Claude API. It records the last request and answers
// with whatever the test sets.
type api struct {
	t        *testing.T
	status   int
	response string
	calls    int
	// request is the last request body, decoded.
	request map[string]any
	header  http.Header
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.calls++
	a.header = r.Header.Clone()
	require.Equal(a.t, "/v1/messages", r.URL.Path)
	body, err := io.ReadAll(r.Body)
	require.NoError(a.t, err)
	a.request = nil
	require.NoError(a.t, json.Unmarshal(body, &a.request), "body: %s", body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.response)
}

// message is a Messages API response whose text is the given string.
func message(text, stopReason string) string {
	body, _ := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": stopReason,
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 10},
	})
	return string(body)
}

func newModel(t *testing.T, a *api) *Model {
	t.Helper()
	a.t = t
	if a.status == 0 {
		a.status = http.StatusOK
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return New("test-key", "", option.WithBaseURL(srv.URL))
}

var choicesRequest = providers.ChoicesRequest{
	RoomID: "room-1", Turn: 3, Attempt: 1,
	Scene:      providers.Scene{Topic: "Who ate the last biscuit", Description: "A cramped kitchen at midnight.", Genre: "comedy"},
	CharacterA: providers.Character{Name: "Grumble", Brief: "A tired swamp monster who denies everything."},
	CharacterB: providers.Character{Name: "Fizz", Brief: "An excitable imp with crumbs on its face."},
	Speaker:    "A", Beat: "REACT",
	History: []providers.Line{
		{Speaker: "A", Text: "I am Grumble, and I have never seen a biscuit."},
		{Speaker: "B", Text: "Then why is the tin empty?"},
	},
	TurnsRemaining: 5,
}

func TestGenerateChoicesAsksWithTheWholeSceneAndParsesThreeLines(t *testing.T) {
	a := &api{response: message(`{"line1":"The tin was always empty.","line2":"Ask the cat.","line3":"Fine. It was delicious."}`, "end_turn")}
	got, err := newModel(t, a).GenerateChoices(context.Background(), choicesRequest)
	require.NoError(t, err)
	require.Equal(t, providers.Choices{
		Lines: []string{"The tin was always empty.", "Ask the cat.", "Fine. It was delicious."},
		Model: "claude-sonnet-5-5",
	}, got, "the lines, and the model that actually answered")

	require.Equal(t, "test-key", a.header.Get("X-Api-Key"))
	require.Equal(t, DefaultModel, a.request["model"])
	require.NotContains(t, a.request, "thinking", "thinking is left at the model's default")
	require.NotContains(t, a.request, "tool_choice")

	config := a.request["output_config"].(map[string]any)
	require.Equal(t, "low", config["effort"])
	format := config["format"].(map[string]any)
	require.Equal(t, "json_schema", format["type"])
	require.ElementsMatch(t, []any{"line1", "line2", "line3"}, format["schema"].(map[string]any)["required"])

	system := a.request["system"].([]any)[0].(map[string]any)["text"].(string)
	require.Contains(t, system, "meaningfully different directions")

	// The user message carries everything that changes from turn to turn.
	messages := a.request["messages"].([]any)
	require.Len(t, messages, 1, "one user turn and no prefilled answer")
	user := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{
		"Who ate the last biscuit", "A cramped kitchen at midnight.", "comedy",
		"Grumble", "A tired swamp monster who denies everything.",
		"Fizz", "An excitable imp with crumbs on its face.",
		"Grumble: I am Grumble, and I have never seen a biscuit.",
		"Fizz: Then why is the tin empty?",
		"turn 3 of 8", "Grumble speaks", "REACT", "5 turns remain",
	} {
		require.Contains(t, user, want)
	}
}

func TestGenerateChoicesSaysWhenTheSceneIsStartingOrEnding(t *testing.T) {
	a := &api{response: message(`{"line1":"a","line2":"b","line3":"c"}`, "end_turn")}
	m := newModel(t, a)
	userText := func() string {
		return a.request["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}

	first := choicesRequest
	first.Turn, first.History, first.TurnsRemaining, first.Beat = 1, nil, 7, "INTRO"
	_, err := m.GenerateChoices(context.Background(), first)
	require.NoError(t, err)
	require.Contains(t, userText(), "first line of the scene")

	last := choicesRequest
	last.Turn, last.Speaker, last.TurnsRemaining, last.Beat = 8, "B", 0, "EXPLAIN + PUNCHLINE"
	_, err = m.GenerateChoices(context.Background(), last)
	require.NoError(t, err)
	require.Contains(t, userText(), "Fizz speaks")
	require.Contains(t, userText(), "final turn")
}

func TestEnhanceTagsSendsTheLineAndReturnsTheTaggedOne(t *testing.T) {
	a := &api{response: message(`{"tagged":"[nervous] Ask the cat."}`, "end_turn")}
	got, err := newModel(t, a).EnhanceTags(context.Background(), providers.TagsRequest{
		RoomID: "room-1", Turn: 3, Attempt: 1, Speaker: "A", Beat: "REACT", Text: "Ask the cat.",
		Character: providers.Character{Name: "Grumble", Brief: "A tired swamp monster who denies everything."},
	})
	require.NoError(t, err)
	require.Equal(t, providers.Tagged{Text: "[nervous] Ask the cat.", Model: "claude-sonnet-5-5"}, got)

	system := a.request["system"].([]any)[0].(map[string]any)["text"].(string)
	require.Contains(t, system, "must stay exactly as given")
	user := a.request["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	require.Contains(t, user, "<line>Ask the cat.</line>")
	require.Contains(t, user, "Grumble")
	require.Contains(t, user, "REACT")
}

func TestAnswersThatAreNotAnAnswerAreOrdinaryFailures(t *testing.T) {
	for name, response := range map[string]string{
		"a refusal":             message("I can't help with that.", "refusal"),
		"cut off at the limit":  message(`{"line1":"a","line2":"b","li`, "max_tokens"),
		"prose instead of JSON": message("Here are three lines for you!", "end_turn"),
	} {
		t.Run(name, func(t *testing.T) {
			a := &api{response: response}
			_, err := newModel(t, a).GenerateChoices(context.Background(), choicesRequest)
			require.Error(t, err)
			require.False(t, providers.IsPermanent(err), "worth another attempt: %v", err)
			require.Equal(t, 1, a.calls)
		})
	}
}

func TestHTTPErrorsAreSortedIntoRetryableAndPermanent(t *testing.T) {
	const errorBody = `{"type":"error","error":{"type":"some_error","message":"no"}}`
	for status, permanent := range map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusUnauthorized:        true,
		http.StatusNotFound:            true,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		529:                            false, // overloaded
	} {
		a := &api{status: status, response: errorBody}
		_, err := newModel(t, a).GenerateChoices(context.Background(), choicesRequest)
		require.Error(t, err, "status %d", status)
		require.Equal(t, permanent, providers.IsPermanent(err), "status %d: %v", status, err)
		require.Equal(t, 1, a.calls, "status %d: the SDK does not retry; the workflow does", status)
	}
}

func TestNewUsesTheConfiguredModel(t *testing.T) {
	a := &api{t: t, status: http.StatusOK, response: message(`{"tagged":"x"}`, "end_turn")}
	srv := httptest.NewServer(a)
	defer srv.Close()
	m := New("k", "claude-haiku-4-5", option.WithBaseURL(srv.URL))
	_, err := m.EnhanceTags(context.Background(), providers.TagsRequest{Text: "x"})
	require.NoError(t, err)
	require.Equal(t, "claude-haiku-4-5", a.request["model"])
}
