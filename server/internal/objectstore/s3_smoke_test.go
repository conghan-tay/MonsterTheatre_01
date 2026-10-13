package objectstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

// TestS3SmokeAgainstARealBucket checks the store against S3 itself, which
// the fake in s3_test.go only imitates. It runs only when S3_SMOKE_BUCKET
// names a bucket, with whatever AWS identity and region the environment
// gives:
//
//	S3_SMOKE_BUCKET=my-bucket AWS_REGION=ap-southeast-1 go test ./internal/objectstore/ -run Smoke -v
//
// It writes one small object under smoke/ and deletes it afterwards.
func TestS3SmokeAgainstARealBucket(t *testing.T) {
	bucket := os.Getenv("S3_SMOKE_BUCKET")
	if bucket == "" {
		t.Skip("S3_SMOKE_BUCKET is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := newS3Client(ctx)
	require.NoError(t, err)
	store := NewS3(client, bucket)

	suffix := make([]byte, 8)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	key := "smoke/" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		_, err := client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		require.NoError(t, err, "delete the smoke object")
	})

	_, err = store.Get(ctx, key)
	require.ErrorIs(t, err, ErrNotFound, "before it is written")

	require.NoError(t, store.Put(ctx, key, strings.NewReader("0123456789"), "audio/mpeg"))

	obj, err := store.Get(ctx, key)
	require.NoError(t, err)
	defer obj.Body.Close()
	require.Equal(t, int64(10), obj.Size)
	require.Equal(t, "audio/mpeg", obj.ContentType)

	whole, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "0123456789", string(whole))

	end, err := obj.Body.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, int64(10), end)
	_, err = obj.Body.Seek(6, io.SeekStart)
	require.NoError(t, err)
	tail, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "6789", string(tail), "a ranged read from the offset sought")
}
