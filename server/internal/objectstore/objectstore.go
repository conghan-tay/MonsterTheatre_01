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
//
// Body can seek, which is what lets the server answer a range request by
// handing it to http.ServeContent: that seeks to the end to learn the size,
// then to the first byte asked for, and reads on from there. An
// implementation over a remote store must make that cheap: answer the seek
// to the end from Size, and open the read at the offset last sought (for S3,
// a ranged GetObject) instead of fetching the object from its start.
type Object struct {
	Body        io.ReadSeekCloser
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
