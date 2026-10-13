package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3API is the part of S3 the store uses. *s3.Client implements it.
type S3API interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	// The last two are used only by DeletePrefix.
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// S3 stores objects in one bucket, under the same keys as the filesystem
// store.
type S3 struct {
	client S3API
	bucket string
}

// NewS3 returns a store over the bucket.
func NewS3(client S3API, bucket string) *S3 {
	return &S3{client: client, bucket: bucket}
}

// Put reads the whole object into memory and sends it in one request. The
// SDK needs a body it can rewind to sign and to retry, and everything stored
// here is small: a portrait of at most a few megabytes, or one turn's
// recording.
func (s *S3) Put(ctx context.Context, key string, body io.Reader, contentType string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read object: %w", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

// Get asks S3 for the object's size and content type only. Nothing of the
// body is fetched until it is read.
func (s *S3) Get(ctx context.Context, key string) (*Object, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if isMissing(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("head object: %w", err)
	}
	size := aws.ToInt64(head.ContentLength)
	return &Object{
		Body:        &s3Body{ctx: ctx, store: s, key: key, size: size},
		Size:        size,
		ContentType: aws.ToString(head.ContentType),
	}, nil
}

// DeletePrefix lists the keys under the prefix a page at a time and deletes
// each page as it goes. A page is at most 1,000 keys, which is also the most
// one delete request may name.
func (s *S3) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if err := checkPrefix(prefix); err != nil {
		return 0, err
	}
	removed := 0
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return removed, fmt.Errorf("list objects: %w", err)
		}
		if len(page.Contents) > 0 {
			keys := make([]types.ObjectIdentifier, len(page.Contents))
			for i, obj := range page.Contents {
				keys[i] = types.ObjectIdentifier{Key: obj.Key}
			}
			out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(s.bucket),
				Delete: &types.Delete{Objects: keys, Quiet: aws.Bool(true)},
			})
			if err != nil {
				return removed, fmt.Errorf("delete objects: %w", err)
			}
			// S3 answers 200 even when it could not delete some of the keys.
			if len(out.Errors) > 0 {
				first := out.Errors[0]
				return removed, fmt.Errorf("delete objects: %d not deleted, the first %s: %s",
					len(out.Errors), aws.ToString(first.Key), aws.ToString(first.Message))
			}
			removed += len(keys)
		}
		if !aws.ToBool(page.IsTruncated) {
			return removed, nil
		}
		token = page.NextContinuationToken
	}
}

// isMissing reports whether S3 said there is no such object. HeadObject and
// GetObject say it with different error types.
func isMissing(err error) bool {
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	return errors.As(err, &notFound) || errors.As(err, &noSuchKey)
}

// s3Body is an object's body that can seek (docs/decisions.md, D040). A seek
// only moves the offset; the seek to the end is answered from the size. The
// first read after a seek opens a GetObject from that offset to the end, and
// later reads continue it.
type s3Body struct {
	// ctx is the one Get was called with: the reads belong to the same
	// request, and end with it.
	ctx    context.Context
	store  *S3
	key    string
	size   int64
	offset int64
	// stream is the open GetObject, positioned at offset. Nil until a read
	// needs it, and again after a seek moves away from it.
	stream io.ReadCloser
}

func (b *s3Body) Read(p []byte) (int, error) {
	// Also what keeps an empty object from being asked for: S3 refuses a
	// range on one.
	if b.offset >= b.size {
		return 0, io.EOF
	}
	if b.stream == nil {
		in := &s3.GetObjectInput{Bucket: aws.String(b.store.bucket), Key: aws.String(b.key)}
		if b.offset > 0 {
			in.Range = aws.String(fmt.Sprintf("bytes=%d-", b.offset))
		}
		out, err := b.store.client.GetObject(b.ctx, in)
		if isMissing(err) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("get object: %w", err)
		}
		b.stream = out.Body
	}
	n, err := b.stream.Read(p)
	b.offset += int64(n)
	return n, err
}

func (b *s3Body) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = b.offset + offset
	case io.SeekEnd:
		target = b.size + offset
	default:
		return 0, fmt.Errorf("seek object: invalid whence %d", whence)
	}
	if target < 0 {
		return 0, errors.New("seek object: before the start")
	}
	if target != b.offset && b.stream != nil {
		// The open stream is at the old offset; the next read opens another.
		b.stream.Close()
		b.stream = nil
	}
	b.offset = target
	return target, nil
}

func (b *s3Body) Close() error {
	if b.stream == nil {
		return nil
	}
	err := b.stream.Close()
	b.stream = nil
	return err
}
