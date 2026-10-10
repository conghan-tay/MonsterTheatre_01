package providers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// ChoicesPerTurn is how many lines the audience chooses between.
	ChoicesPerTurn = 3
	// MaxLineLength bounds a generated line, in characters
	// (docs/system-design.md, section 5).
	MaxLineLength = 300
	// maxTagAllowance is how much longer than its line a tagged line may be.
	maxTagAllowance = 120
)

// audioTag matches one audio tag: square brackets around a short direction.
var audioTag = regexp.MustCompile(`\[[^\[\]]*\]`)

// ValidateChoices checks a model's candidate lines and returns them trimmed.
// There must be exactly three, each non-empty, at most MaxLineLength
// characters, on one line, without square brackets, and all different.
//
// Square brackets are refused because they mark audio tags: a line that
// already had them could not be told apart from its tagged version.
func ValidateChoices(lines []string) ([]string, error) {
	if len(lines) != ChoicesPerTurn {
		return nil, fmt.Errorf("the model returned %d lines, not %d", len(lines), ChoicesPerTurn)
	}
	cleaned := make([]string, len(lines))
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			return nil, fmt.Errorf("line %d is empty", i+1)
		case utf8.RuneCountInString(line) > MaxLineLength:
			return nil, fmt.Errorf("line %d is longer than %d characters", i+1, MaxLineLength)
		case strings.ContainsAny(line, "\n\r"):
			return nil, fmt.Errorf("line %d spans more than one line", i+1)
		case strings.ContainsAny(line, "[]"):
			return nil, fmt.Errorf("line %d contains square brackets", i+1)
		}
		key := strings.ToLower(collapseSpaces(line))
		if seen[key] {
			return nil, fmt.Errorf("line %d repeats another line", i+1)
		}
		seen[key] = true
		cleaned[i] = line
	}
	return cleaned, nil
}

// ValidateTagged checks that a tagged line is the original with audio tags
// inserted and nothing else changed, and returns it trimmed. With the tags
// taken out, what is left must be the original, word for word; only the
// spacing around a tag may differ.
func ValidateTagged(original, tagged string) (string, error) {
	tagged = strings.TrimSpace(tagged)
	if tagged == "" {
		return "", errors.New("the tagged line is empty")
	}
	if strings.ContainsAny(tagged, "\n\r") {
		return "", errors.New("the tagged line spans more than one line")
	}
	if utf8.RuneCountInString(tagged) > utf8.RuneCountInString(original)+maxTagAllowance {
		return "", errors.New("the tagged line is much longer than the original")
	}
	if collapseSpaces(audioTag.ReplaceAllString(tagged, " ")) != collapseSpaces(original) {
		return "", errors.New("the tagged line does not keep the original words")
	}
	return tagged, nil
}

// collapseSpaces trims s and turns each run of white space into one space.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
