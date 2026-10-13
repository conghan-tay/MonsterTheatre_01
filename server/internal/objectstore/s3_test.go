package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

// fakeS3 keeps objects in memory and records what was asked of it.
type fakeS3 struct {
	objects map[string]fakeObject
	// ranges holds the Range of every GetObject, "" for a whole read.
	ranges []string
	heads  int
	puts   []*s3.PutObjectInput
}

type fakeObject struct {
	body        []byte
	contentType string
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string]fakeObject{}} }

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.heads++
	obj, ok := f.objects[*in.Bucket+"/"+*in.Key]
	if !ok {
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(obj.body))),
		ContentType:   aws.String(obj.contentType),
	}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	obj, ok := f.objects[*in.Bucket+"/"+*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	f.ranges = append(f.ranges, aws.ToString(in.Range))
	from := 0
	if in.Range != nil {
		if _, err := fmt.Sscanf(*in.Range, "bytes=%d-", &from); err != nil {
			return nil, fmt.Errorf("fake S3: unexpected range %q", *in.Range)
		}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(obj.body[from:]))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.puts = append(f.puts, in)
	f.objects[*in.Bucket+"/"+*in.Key] = fakeObject{body: body, contentType: aws.ToString(in.ContentType)}
	return &s3.PutObjectOutput{}, nil
}

func TestS3PutThenGetReturnsBodySizeAndContentType(t *testing.T) {
	api := newFakeS3()
	store := NewS3(api, "theatre")
	ctx := context.Background()

	require.NoError(t, store.Put(ctx, "rooms/r1/portrait-a", strings.NewReader("png-bytes"), "image/png"))

	require.Len(t, api.puts, 1)
	require.Equal(t, "theatre", *api.puts[0].Bucket)
	require.Equal(t, "rooms/r1/portrait-a", *api.puts[0].Key)
	require.Equal(t, "image/png", *api.puts[0].ContentType)
	require.Equal(t, int64(9), *api.puts[0].ContentLength, "the length is sent, so S3 need not be told to expect a stream")

	obj, err := store.Get(ctx, "rooms/r1/portrait-a")
	require.NoError(t, err)
	defer obj.Body.Close()
	require.Equal(t, int64(9), obj.Size)
	require.Equal(t, "image/png", obj.ContentType)

	body, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "png-bytes", string(body))
	require.Equal(t, []string{""}, api.ranges, "a read from the start is one whole GetObject")
}

func TestS3PutReplacesExistingObject(t *testing.T) {
	store := NewS3(newFakeS3(), "theatre")
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

func TestS3GetMissingObjectIsNotFound(t *testing.T) {
	store := NewS3(newFakeS3(), "theatre")
	_, err := store.Get(context.Background(), "rooms/r1/portrait-b")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestS3RefusesInvalidKeysWithoutCallingS3(t *testing.T) {
	api := newFakeS3()
	store := NewS3(api, "theatre")
	ctx := context.Background()

	for _, key := range []string{"", "/rooms/r1", "rooms/../secrets", "rooms//r1", "rooms\\r1", "./rooms"} {
		_, err := store.Get(ctx, key)
		require.ErrorIs(t, err, ErrInvalidKey, "get %q", key)
		require.ErrorIs(t, store.Put(ctx, key, strings.NewReader("x"), "text/plain"), ErrInvalidKey, "put %q", key)
	}
	require.Zero(t, api.heads)
	require.Empty(t, api.puts)
}

// D040: the seek to the end is answered from the size, and the read opens
// at the offset last sought.
func TestS3BodySeeksWithoutReadingAndOpensAtTheOffset(t *testing.T) {
	api := newFakeS3()
	store := NewS3(api, "theatre")
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "performances/p1/turn-1.mp3", strings.NewReader("0123456789"), "audio/mpeg"))

	obj, err := store.Get(ctx, "performances/p1/turn-1.mp3")
	require.NoError(t, err)
	defer obj.Body.Close()

	end, err := obj.Body.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, int64(10), end)
	at, err := obj.Body.Seek(4, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(4), at)
	require.Empty(t, api.ranges, "seeking fetches nothing")

	rest, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "456789", string(rest))
	require.Equal(t, []string{"bytes=4-"}, api.ranges, "one ranged GetObject, from the offset sought")
}

func TestS3BodySeekingAfterAReadOpensAgainAtTheNewOffset(t *testing.T) {
	api := newFakeS3()
	store := NewS3(api, "theatre")
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "k", strings.NewReader("0123456789"), "text/plain"))

	obj, err := store.Get(ctx, "k")
	require.NoError(t, err)
	defer obj.Body.Close()

	first := make([]byte, 3)
	_, err = io.ReadFull(obj.Body, first)
	require.NoError(t, err)
	require.Equal(t, "012", string(first))

	at, err := obj.Body.Seek(2, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(5), at)
	rest, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "56789", string(rest))
	require.Equal(t, []string{"", "bytes=5-"}, api.ranges)

	_, err = obj.Body.Seek(-1, io.SeekStart)
	require.Error(t, err, "a seek before the start is refused")
}

func TestS3BodyAtOrPastTheEndReadsNothingAndFetchesNothing(t *testing.T) {
	api := newFakeS3()
	store := NewS3(api, "theatre")
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "empty", strings.NewReader(""), "text/plain"))

	obj, err := store.Get(ctx, "empty")
	require.NoError(t, err)
	defer obj.Body.Close()
	body, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Empty(t, body)
	require.Empty(t, api.ranges, "an empty object is never fetched: S3 refuses a range on one")
}

// The use the seekable body exists for: the standard library answering a
// range request.
func TestS3BodyAnswersARangeRequestThroughServeContent(t *testing.T) {
	api := newFakeS3()
	store := NewS3(api, "theatre")
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "clip", strings.NewReader("0123456789"), "audio/mpeg"))

	obj, err := store.Get(ctx, "clip")
	require.NoError(t, err)
	defer obj.Body.Close()

	req := httptest.NewRequest(http.MethodGet, "/clip", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", obj.ContentType)
	http.ServeContent(rec, req, "", time.Time{}, obj.Body)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "bytes 2-5/10", rec.Header().Get("Content-Range"))
	require.Equal(t, "2345", rec.Body.String())
	require.Equal(t, []string{"bytes=2-"}, api.ranges)
}
