// Package objectstore stores portraits and turn recordings.
//
// The filesystem implementation here serves local development and tests. The
// S3 implementation is added with the AWS deployment.
package objectstore

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Get when no object exists at the key.
var ErrNotFound = errors.New("object not found")

// ErrInvalidKey is returned when a key is empty or would escape the store.
var ErrInvalidKey = errors.New("invalid object key")

// Object is a stored object opened for reading. The caller closes Body.
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// Store reads and writes objects by key, for example
// "rooms/{roomId}/portrait-a" or "performances/{id}/turn-3.mp3".
type Store interface {
	// Put writes the object, replacing any existing one at the key.
	Put(ctx context.Context, key string, body io.Reader, contentType string) error
	// Get opens the object, or returns ErrNotFound.
	Get(ctx context.Context, key string) (*Object, error)
}
