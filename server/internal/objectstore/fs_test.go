package objectstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) *FS {
	t.Helper()
	store, err := NewFS(t.TempDir())
	require.NoError(t, err)
	return store
}

func TestFSPutThenGetReturnsBodySizeAndContentType(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	require.NoError(t, store.Put(ctx, "rooms/r1/portrait-a", strings.NewReader("png-bytes"), "image/png"))

	obj, err := store.Get(ctx, "rooms/r1/portrait-a")
	require.NoError(t, err)
	defer obj.Body.Close()

	body, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "png-bytes", string(body))
	require.Equal(t, int64(9), obj.Size)
	require.Equal(t, "image/png", obj.ContentType)
}

func TestFSPutReplacesExistingObject(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	key := "performances/p1/turn-1.mp3"

	require.NoError(t, store.Put(ctx, key, strings.NewReader("first attempt"), "audio/mpeg"))
	require.NoError(t, store.Put(ctx, key, strings.NewReader("retry"), "audio/mpeg"))

	obj, err := store.Get(ctx, key)
	require.NoError(t, err)
	defer obj.Body.Close()
	body, _ := io.ReadAll(obj.Body)
	require.Equal(t, "retry", string(body))
}

func TestFSGetMissingObjectIsNotFound(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "rooms/r1/portrait-a", strings.NewReader("x"), "image/png"))

	_, err := store.Get(ctx, "rooms/r1/portrait-b")
	require.ErrorIs(t, err, ErrNotFound)

	// A key that names a directory is not an object.
	_, err = store.Get(ctx, "rooms/r1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFSRejectsKeysThatEscapeTheRoot(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for _, key := range []string{
		"",
		"/etc/passwd",
		"../outside",
		"rooms/../../outside",
		"rooms/./r1",
		"rooms//r1",
		`rooms\r1`,
		"rooms/r1/portrait-a.contenttype",
	} {
		require.ErrorIs(t, store.Put(ctx, key, strings.NewReader("x"), "text/plain"), ErrInvalidKey, "put %q", key)
		_, err := store.Get(ctx, key)
		require.ErrorIs(t, err, ErrInvalidKey, "get %q", key)
	}
}

func TestFSPutLeavesNoTemporaryFiles(t *testing.T) {
	store := newStore(t)
	require.NoError(t, store.Put(context.Background(), "a/b", strings.NewReader("x"), "text/plain"))

	entries, err := os.ReadDir(filepath.Join(store.root, "a"))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.ElementsMatch(t, []string{"b", "b.contenttype"}, names)
}

// A range request is answered by seeking: to the end for the size, then to
// the first byte asked for.
func TestFSGetReturnsABodyThatSeeks(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "performances/p1/turn-1.mp3", strings.NewReader("0123456789"), "audio/mpeg"))

	obj, err := store.Get(ctx, "performances/p1/turn-1.mp3")
	require.NoError(t, err)
	defer obj.Body.Close()

	end, err := obj.Body.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, obj.Size, end)

	_, err = obj.Body.Seek(6, io.SeekStart)
	require.NoError(t, err)
	rest, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "6789", string(rest))
}
