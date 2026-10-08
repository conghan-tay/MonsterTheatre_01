package rooms

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// MinShareLinkSecretLength is the shortest secret accepted.
const MinShareLinkSecretLength = 32

// shareLinkContext separates this use of the secret from any other, so the
// same secret could never produce a valid token for something else.
const shareLinkContext = "monstertheatre/share-link/v1\x00"

// ShareLinks builds and checks audience share links.
//
// A link's token is an HMAC of the room ID under a server secret, so nothing
// about the link is stored: it is rebuilt whenever the director's page needs
// it and checked by recomputing (docs/decisions.md, D034). Changing the
// secret invalidates every existing link.
type ShareLinks struct {
	secret  []byte
	baseURL string
}

// NewShareLinks returns a ShareLinks, or an error if the secret is too short.
func NewShareLinks(secret, publicBaseURL string) (ShareLinks, error) {
	if len(secret) < MinShareLinkSecretLength {
		return ShareLinks{}, errors.New("SHARE_LINK_SECRET must be set to at least 32 characters; " +
			"generate one with `openssl rand -hex 32` (`make dev` does this for you)")
	}
	return ShareLinks{secret: []byte(secret), baseURL: publicBaseURL}, nil
}

// Token returns the room's share-link token.
func (s ShareLinks) Token(roomID string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(shareLinkContext))
	mac.Write([]byte(roomID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// URL returns the link an audience member opens to join the room.
func (s ShareLinks) URL(roomID string) string {
	return s.baseURL + "/join/" + roomID + "/" + s.Token(roomID)
}

// Verify reports whether token is the room's share-link token, in constant time.
func (s ShareLinks) Verify(roomID, token string) bool {
	return hmac.Equal([]byte(token), []byte(s.Token(roomID)))
}
