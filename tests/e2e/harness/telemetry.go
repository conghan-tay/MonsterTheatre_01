package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The helpers below let the telemetry tests break one part of the stack
// and read what each process logged about it.

// CutSecondServerRedis closes the second server's connections to Redis and
// refuses new ones, as a network fault would. The worker and the first
// server are untouched.
func (s *Stack) CutSecondServerRedis() error {
	if s.redisProxy == nil {
		return errors.New("the second server does not go through a proxy")
	}
	s.redisProxy.cut()
	return nil
}

// RestoreSecondServerRedis lets the second server reach Redis again.
func (s *Stack) RestoreSecondServerRedis() error {
	if s.redisProxy == nil {
		return errors.New("the second server does not go through a proxy")
	}
	s.redisProxy.restore()
	return nil
}

// LogLine is one JSON line a process logged.
type LogLine map[string]any

// String is a field as text, or "".
func (l LogLine) String(key string) string {
	v, _ := l[key].(string)
	return v
}

// Number is a numeric field, or -1.
func (l LogLine) Number(key string) float64 {
	v, ok := l[key].(float64)
	if !ok {
		return -1
	}
	return v
}

// LogLines returns the lines with the given message that a process
// ("server", "server-2", or "worker") has logged so far, in order, and
// keeps those for which keep is true; nil keeps all.
func (s *Stack) LogLines(process, msg string, keep func(LogLine) bool) ([]LogLine, error) {
	if s.Remote {
		return nil, errors.New("a deployment's logs are in CloudWatch")
	}
	data, err := os.ReadFile(filepath.Join(s.LogDir, process+".log"))
	if err != nil {
		return nil, err
	}
	var out []LogLine
	for _, text := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var line LogLine
		if json.Unmarshal([]byte(text), &line) != nil || line.String("msg") != msg {
			continue
		}
		if keep == nil || keep(line) {
			out = append(out, line)
		}
	}
	return out, nil
}

// BlockObject makes every upload to key fail, by putting a directory with
// something in it where the object would go: the store writes to a
// temporary file and renames it into place, and no file can be renamed
// over a directory that is not empty.
func (s *Stack) BlockObject(key string) error {
	if s.Remote {
		return errors.New("a deployment's objects are in S3")
	}
	dir := filepath.Join(s.workDir, "objects", filepath.FromSlash(key))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("block %s: %w", key, err)
	}
	return os.WriteFile(filepath.Join(dir, "blocked"), []byte("in the way"), 0o644)
}
