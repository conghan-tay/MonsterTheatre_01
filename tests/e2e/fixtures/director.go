// Package fixtures creates test data. Every test makes its own, so tests
// never share state and nothing needs cleaning up between them.
package fixtures

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"
)

// Director is a director account's credentials.
type Director struct {
	Username string
	Password string
}

// DirectorAdder creates director accounts. The harness Stack implements it.
type DirectorAdder interface {
	AddDirector(ctx context.Context, username, password string) error
}

// NewDirector creates a director account unique to this test and returns
// its credentials.
func NewDirector(t testing.TB, stack DirectorAdder) Director {
	t.Helper()
	d := Director{
		Username: "director-" + randomHex(t, 6),
		Password: "pw-" + randomHex(t, 12),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := stack.AddDirector(ctx, d.Username, d.Password); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return d
}

func randomHex(t testing.TB, bytes int) string {
	t.Helper()
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}
