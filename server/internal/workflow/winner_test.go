package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

var three = []performance.Choice{{ID: "c1", Text: "one"}, {ID: "c2", Text: "two"}, {ID: "c3", Text: "three"}}

func ids(choices []performance.Choice) []string {
	var out []string
	for _, c := range choices {
		out = append(out, c.ID)
	}
	return out
}

func TestLeaders(t *testing.T) {
	cases := []struct {
		name      string
		counts    map[string]int
		leaders   []string
		decidedBy string
	}{
		{"a clear winner", map[string]int{"c1": 1, "c2": 5, "c3": 2}, []string{"c2"}, performance.DecidedByMajority},
		{"the most votes wins without more than half", map[string]int{"c1": 4, "c2": 3, "c3": 3}, []string{"c1"}, performance.DecidedByMajority},
		{"a single vote decides", map[string]int{"c1": 0, "c2": 0, "c3": 1}, []string{"c3"}, performance.DecidedByMajority},
		{"a two-way tie leaves out the third", map[string]int{"c1": 2, "c2": 0, "c3": 2}, []string{"c1", "c3"}, performance.DecidedByTie},
		{"a three-way tie", map[string]int{"c1": 1, "c2": 1, "c3": 1}, []string{"c1", "c2", "c3"}, performance.DecidedByTie},
		{"an empty round", map[string]int{"c1": 0, "c2": 0, "c3": 0}, []string{"c1", "c2", "c3"}, performance.DecidedByNoVotes},
		{"no counts at all", nil, []string{"c1", "c2", "c3"}, performance.DecidedByNoVotes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top, decidedBy := leaders(three, tc.counts)
			require.Equal(t, tc.leaders, ids(top), "leaders, in the order the choices were offered")
			require.Equal(t, tc.decidedBy, decidedBy)
		})
	}
}
