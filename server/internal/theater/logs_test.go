package theater

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// lockedBuffer is a log destination that the code's goroutines and the
// test can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// lines are the JSON log lines with the given message.
func (l *lockedBuffer) lines(msg string) []map[string]any {
	l.mu.Lock()
	text := l.b.String()
	l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}
