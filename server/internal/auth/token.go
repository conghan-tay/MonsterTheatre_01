package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// NewToken returns a new session secret and the hash to store for it. The
// secret goes to the browser in a cookie; only the hash is kept, so a leaked
// database does not contain usable credentials (docs/decisions.md, D030).
//
// A fast hash is enough here, unlike for passwords: the secret is 32 random
// bytes and cannot be guessed.
func NewToken() (token string, hash []byte, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(secret)
	return token, HashToken(token), nil
}

// HashToken returns the stored form of a session secret.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
