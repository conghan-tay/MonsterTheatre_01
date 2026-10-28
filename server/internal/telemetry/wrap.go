package telemetry

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// The wrappers below time each call to a provider or the object store and
// log one line for it, "provider call" or "object put", with the IDs of the
// activity attempt that made it. They change nothing else: answers and
// errors are passed on as they are.

// Dialogue times a language model's calls.
type Dialogue struct {
	Next     providers.DialogueModel
	Provider string
	Logger   *slog.Logger
	Now      func() time.Time
}

func (d Dialogue) GenerateChoices(ctx context.Context, req providers.ChoicesRequest) (out providers.Choices, err error) {
	err = call(ctx, d.Logger, d.Now, d.Provider, "choices", func() error {
		out, err = d.Next.GenerateChoices(ctx, req)
		return err
	})
	return out, err
}

func (d Dialogue) EnhanceTags(ctx context.Context, req providers.TagsRequest) (out providers.Tagged, err error) {
	err = call(ctx, d.Logger, d.Now, d.Provider, "tags", func() error {
		out, err = d.Next.EnhanceTags(ctx, req)
		return err
	})
	return out, err
}

// Speech times a speech service's calls.
type Speech struct {
	Next     providers.Speech
	Provider string
	Logger   *slog.Logger
	Now      func() time.Time
}

func (s Speech) Synthesize(ctx context.Context, req providers.SpeechRequest) (out []byte, err error) {
	err = call(ctx, s.Logger, s.Now, s.Provider, "speech", func() error {
		out, err = s.Next.Synthesize(ctx, req)
		return err
	})
	return out, err
}

// LipSync times a lip-sync service's calls.
type LipSync struct {
	Next     providers.LipSync
	Provider string
	Logger   *slog.Logger
	Now      func() time.Time
}

func (l LipSync) Upload(ctx context.Context, req providers.UploadRequest) (out string, err error) {
	err = call(ctx, l.Logger, l.Now, l.Provider, "lipsync_upload", func() error {
		out, err = l.Next.Upload(ctx, req)
		return err
	})
	return out, err
}

func (l LipSync) Submit(ctx context.Context, req providers.LipSyncRequest) (out string, err error) {
	err = call(ctx, l.Logger, l.Now, l.Provider, "lipsync_submit", func() error {
		out, err = l.Next.Submit(ctx, req)
		return err
	})
	return out, err
}

func (l LipSync) Result(ctx context.Context, id string) (out providers.LipSyncResult, err error) {
	err = call(ctx, l.Logger, l.Now, l.Provider, "lipsync_result", func() error {
		out, err = l.Next.Result(ctx, id)
		return err
	})
	return out, err
}

func (l LipSync) Download(ctx context.Context, url string) (out []byte, err error) {
	err = call(ctx, l.Logger, l.Now, l.Provider, "lipsync_download", func() error {
		out, err = l.Next.Download(ctx, url)
		return err
	})
	return out, err
}

func call(ctx context.Context, logger *slog.Logger, now func() time.Time, provider, op string, fn func() error) error {
	if now == nil {
		now = time.Now
	}
	return timed(ctx, logger, now, "provider call", []any{"provider", provider, "op", op}, fn)
}

// Store times uploads to the object store. Reads are not logged.
type Store struct {
	Next   objectstore.Store
	Logger *slog.Logger
	Now    func() time.Time
}

func (s Store) Put(ctx context.Context, key string, body io.Reader, contentType string) error {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	counted := &counter{r: body}
	start := now()
	err := s.Next.Put(ctx, key, counted, contentType)
	ended := outcome(err, ctx.Err())
	fields := append(append([]any{}, Fields(ctx)...),
		"key", key, "bytes", counted.n,
		"duration_ms", now().Sub(start).Milliseconds(), "outcome", ended)
	switch {
	case err != nil:
		s.Logger.Warn("object put", append(fields, "error", err.Error())...)
	case ended != "ok":
		s.Logger.Warn("object put", fields...)
	default:
		s.Logger.Info("object put", fields...)
	}
	return err
}

func (s Store) Get(ctx context.Context, key string) (*objectstore.Object, error) {
	return s.Next.Get(ctx, key)
}

// counter counts the bytes read through it.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
