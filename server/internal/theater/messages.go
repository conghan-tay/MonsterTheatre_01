package theater

import (
	"encoding/json"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

// audienceMessage encodes who is in a room's audience, for its director:
// how many have joined, and what they are called, in the order they joined.
func audienceMessage(nicknames []string) []byte {
	if nicknames == nil {
		nicknames = []string{} // an empty list, not null, for the page
	}
	return encode(struct {
		Type      string   `json:"type"`
		Count     int      `json:"count"`
		Nicknames []string `json:"nicknames"`
	}{performance.TypeAudience, len(nicknames), nicknames})
}

// snapshotMessage encodes a snapshot for a browser.
func snapshotMessage(revision int, state performance.State) []byte {
	data, err := json.Marshal(performance.Message{Type: performance.TypeSnapshot, Revision: revision, State: state})
	if err != nil {
		panic(err) // cannot happen: the state holds only plain values
	}
	return data
}
