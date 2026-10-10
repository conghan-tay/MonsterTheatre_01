package providers

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateChoices(t *testing.T) {
	const limit = 160
	long := strings.Repeat("é", limit) // characters, not bytes
	cases := []struct {
		name  string
		lines []string
		ok    bool
	}{
		{"three different lines", []string{"I did not eat it.", "What biscuit?", "It was the cat."}, true},
		{"a line at the length limit", []string{long, "b", "c"}, true},
		{"two lines", []string{"a", "b"}, false},
		{"four lines", []string{"a", "b", "c", "d"}, false},
		{"none", nil, false},
		{"an empty line", []string{"a", "", "c"}, false},
		{"a line of spaces", []string{"a", "   ", "c"}, false},
		{"a line over the limit", []string{long + "é", "b", "c"}, false},
		{"the same line twice", []string{"Hello.", "b", "Hello."}, false},
		{"the same line in another case and spacing", []string{"Hello  there.", "b", "hello there."}, false},
		{"a line with a line break", []string{"one\ntwo", "b", "c"}, false},
		{"a line that already has an audio tag", []string{"[laughs] Oh no.", "b", "c"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateChoices(tc.lines, limit)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}

	cleaned, err := ValidateChoices([]string{"  padded.  ", "b", "c"}, limit)
	require.NoError(t, err)
	require.Equal(t, []string{"padded.", "b", "c"}, cleaned, "lines come back trimmed")
}

func TestValidateTagged(t *testing.T) {
	const original = "I did not eat it. What biscuit?"
	cases := []struct {
		name   string
		tagged string
		ok     bool
	}{
		{"a tag at the start", "[nervous] I did not eat it. What biscuit?", true},
		{"tags in the middle and at the end", "I did not eat it. [gulps] What biscuit? [laughs]", true},
		{"a tag with no space after it", "[nervous]I did not eat it. What biscuit?", true},
		{"unchanged", original, true},
		{"padded", "  [sighs] I did not eat it. What biscuit?  ", true},
		{"a word changed", "[nervous] I did not eat that. What biscuit?", false},
		{"a word added", "[nervous] Well, I did not eat it. What biscuit?", false},
		{"a sentence dropped", "[nervous] I did not eat it.", false},
		{"punctuation changed", "[nervous] I did not eat it! What biscuit?", false},
		{"capitals changed", "[nervous] I DID NOT eat it. What biscuit?", false},
		{"an unclosed tag", "[nervous I did not eat it. What biscuit?", false},
		{"a stage direction in other brackets", "(nervous) I did not eat it. What biscuit?", false},
		{"empty", "", false},
		{"only tags", "[nervous] [laughs]", false},
		{"two lines", "[nervous] I did not eat it.\nWhat biscuit?", false},
		{"far too many tags", strings.Repeat("[very long and elaborate direction] ", 5) + original, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateTagged(original, tc.tagged)
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, strings.TrimSpace(tc.tagged), got)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestPermanent(t *testing.T) {
	base := errors.New("401 unauthorized")
	require.False(t, IsPermanent(base))
	require.True(t, IsPermanent(Permanent(base)))
	require.ErrorIs(t, Permanent(base), base, "the cause stays reachable")
	require.NoError(t, Permanent(nil))

	for status, permanent := range map[int]bool{
		400: true, 401: true, 403: true, 404: true, 422: true,
		408: false, 429: false, 500: false, 502: false, 503: false, 529: false, 200: false,
	} {
		require.Equal(t, permanent, PermanentStatus(status), "status %d", status)
	}
}

func TestValidateChoicesHoldsLinesToTheGivenLimit(t *testing.T) {
	lines := []string{strings.Repeat("a", 100), "b", "c"}
	_, err := ValidateChoices(lines, 100)
	require.NoError(t, err, "exactly at the limit")
	_, err = ValidateChoices(lines, 99)
	require.Error(t, err, "one over")
	_, err = ValidateChoices(lines, 300)
	require.NoError(t, err, "the same line under a higher limit")
}

func TestLineLimit(t *testing.T) {
	// The length to ask the model for is five sixths of the limit, rounded
	// down to a ten, so an answer that runs a little long still passes.
	for limit, target := range map[int]int{
		DefaultLineLimit: 130,
		HighestLineLimit: 250,
		LowestLineLimit:  50,
		120:              100,
		200:              160,
	} {
		got, err := LineLimit(limit)
		require.NoError(t, err, "limit %d", limit)
		require.Equal(t, target, got, "limit %d", limit)
		require.Less(t, got, limit)
	}

	// The setting has guard rails: a typo must not start a worker.
	for _, bad := range []int{LowestLineLimit - 1, HighestLineLimit + 1, 0, -160, 20, 1600} {
		_, err := LineLimit(bad)
		require.Error(t, err, "limit %d", bad)
	}

	require.Equal(t, 160, DefaultLineLimit)
	require.Equal(t, 60, LowestLineLimit)
	require.Equal(t, 300, HighestLineLimit)
}
