package providers

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateChoices(t *testing.T) {
	long := strings.Repeat("é", MaxLineLength) // characters, not bytes
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
			_, err := ValidateChoices(tc.lines)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}

	cleaned, err := ValidateChoices([]string{"  padded.  ", "b", "c"})
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
