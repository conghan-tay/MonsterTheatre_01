package theater

import "encoding/json"

// Message types sent to browsers (docs/system-design.md, section 5).
const typeSnapshot = "snapshot"

// PhaseWaiting is the phase of a room whose director has not pressed Start.
// It appears only in snapshots: nothing publishes it.
const PhaseWaiting = "waiting"

// State is the performance state a snapshot or update carries. Later steps
// add the choices, deadline, votes, and winners.
type State struct {
	Phase string `json:"phase"`
	Turn  int    `json:"turn"`
}

type message struct {
	Type     string `json:"type"`
	Revision int    `json:"revision"`
	State    State  `json:"state"`
}

// waitingSnapshot is the snapshot of a room with no performance: revision 0,
// so the first published update (revision 1) is always newer.
func waitingSnapshot() []byte {
	data, err := json.Marshal(message{Type: typeSnapshot, Revision: 0, State: State{Phase: PhaseWaiting}})
	if err != nil {
		panic(err) // cannot happen: the value is fixed
	}
	return data
}
