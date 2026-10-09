package theater

import (
	"encoding/json"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

// snapshotMessage encodes a snapshot for a browser.
func snapshotMessage(revision int, state performance.State) []byte {
	data, err := json.Marshal(performance.Message{Type: performance.TypeSnapshot, Revision: revision, State: state})
	if err != nil {
		panic(err) // cannot happen: the state holds only plain values
	}
	return data
}
