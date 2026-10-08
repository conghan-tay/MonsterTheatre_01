// Package fixtures creates test data. Every test makes its own, so tests
// never share state and nothing needs cleaning up between them.
package fixtures

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// Director is a director account's credentials.
type Director struct {
	Username string
	Password string
}

// NewDirector returns credentials for a director unique to this test.
//
// For now it only generates the credentials. The directors table and the
// add-director command arrive in step 4; from then on this function also
// creates the account.
func NewDirector(t testing.TB) Director {
	t.Helper()
	return Director{
		Username: "director-" + randomHex(t, 6),
		Password: "pw-" + randomHex(t, 12),
	}
}

func randomHex(t testing.TB, bytes int) string {
	t.Helper()
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}
