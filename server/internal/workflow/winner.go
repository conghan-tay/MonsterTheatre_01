package workflow

import "github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"

// leaders returns the choices with the most votes, in the order they were
// offered, and how the round is decided:
//
//   - one leader with at least one vote: it wins by majority (the most
//     votes, not necessarily more than half);
//   - several leaders with at least one vote each: a tie between them;
//   - no votes at all: every choice leads, and the round has no votes.
//
// The caller picks among several leaders at random.
func leaders(choices []performance.Choice, counts map[string]int) ([]performance.Choice, string) {
	most := 0
	for _, choice := range choices {
		most = max(most, counts[choice.ID])
	}
	if most == 0 {
		return choices, performance.DecidedByNoVotes
	}
	var top []performance.Choice
	for _, choice := range choices {
		if counts[choice.ID] == most {
			top = append(top, choice)
		}
	}
	if len(top) == 1 {
		return top, performance.DecidedByMajority
	}
	return top, performance.DecidedByTie
}
