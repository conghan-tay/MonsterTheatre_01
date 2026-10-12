package theater

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

const (
	// streamBlock is how long one read waits for new entries. It only
	// bounds how quickly Run notices that it should stop.
	streamBlock = 5 * time.Second
	// streamRetryDelay is the pause after a failed read.
	streamRetryDelay = time.Second
	streamBatch      = 100
)

// StreamClient is the part of Redis the reader uses. *redis.Client
// implements it.
type StreamClient interface {
	XRevRangeN(ctx context.Context, stream, start, stop string, count int64) *redis.XMessageSliceCmd
	XRead(ctx context.Context, a *redis.XReadArgs) *redis.XStreamSliceCmd
}

// Broadcaster delivers an update to a room's connections. *Hub implements it.
type Broadcaster interface {
	Broadcast(roomID string, revision int, msg []byte)
}

// NoticeHandler acts on a notice read from the notice stream. *Service
// implements it.
type NoticeHandler interface {
	HandleNotice(ctx context.Context, entry redis.XMessage)
}

// StreamReader reads the two Redis streams that carry what browsers are
// sent, and hands each entry on.
//
// The worker appends each performance update once; this reads each once and
// hands it to every connection in its room (docs/decisions.md, D024). The
// servers append notices, news that one of them has for the others; this
// hands each to the notice handler (docs/decisions.md, D046).
//
// Every server runs one of these and reads both streams whole, so a room's
// people can be spread over any number of servers.
type StreamReader struct {
	Redis StreamClient
	Hub   Broadcaster
	// Notices is given the notice stream's entries. Nil means that stream
	// is not read.
	Notices NoticeHandler
	Logger  *slog.Logger
	// RetryDelay defaults to streamRetryDelay when zero.
	RetryDelay time.Duration
}

// Run reads until ctx ends. It starts from each stream's end: older entries
// are not needed, because a connecting browser's snapshot comes from the
// workflow, and a connecting director is sent the audience.
//
// It then always reads on from the last entry it has seen in each stream.
// Asking Redis for "whatever is new" on each call instead would lose
// anything that arrived between two calls. After a Redis outage it resumes
// from the same places.
func (s *StreamReader) Run(ctx context.Context) {
	delay := s.RetryDelay
	if delay <= 0 {
		delay = streamRetryDelay
	}

	// Where each stream has been read up to, in a fixed order.
	keys := []string{performance.StreamKey}
	if s.Notices != nil {
		keys = append(keys, NoticeStreamKey)
	}
	lastID := make(map[string]string, len(keys))
	for _, key := range keys {
		for lastID[key] == "" {
			newest, err := s.Redis.XRevRangeN(ctx, key, "+", "-", 1).Result()
			switch {
			case err != nil:
				if !s.pause(ctx, delay, "find the end of the "+key+" stream", err) {
					return
				}
			case len(newest) == 0:
				lastID[key] = "0-0" // an empty stream: everything in it will be new
			default:
				lastID[key] = newest[0].ID
			}
		}
	}

	for ctx.Err() == nil {
		// XREAD takes the stream names, then a position for each.
		args := append([]string{}, keys...)
		for _, key := range keys {
			args = append(args, lastID[key])
		}
		streams, err := s.Redis.XRead(ctx, &redis.XReadArgs{
			Streams: args,
			Count:   streamBatch,
			Block:   streamBlock,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue // nothing new within the block time
		}
		if err != nil {
			if !s.pause(ctx, delay, "read the streams", err) {
				return
			}
			continue
		}
		for _, stream := range streams {
			for _, entry := range stream.Messages {
				lastID[stream.Stream] = entry.ID
				if stream.Stream == NoticeStreamKey {
					s.Notices.HandleNotice(ctx, entry)
				} else {
					s.forward(entry)
				}
			}
		}
	}
}

// forward hands one stream entry to its room. An entry that cannot be read
// is logged and skipped; browsers notice the missing revision.
func (s *StreamReader) forward(entry redis.XMessage) {
	roomID, _ := entry.Values[performance.StreamFieldRoomID].(string)
	message, _ := entry.Values[performance.StreamFieldMessage].(string)
	revisionText, _ := entry.Values[performance.StreamFieldRevision].(string)
	revision, err := strconv.Atoi(revisionText)
	if roomID == "" || message == "" || err != nil {
		s.Logger.Error("theater: malformed update in stream", "stream_id", entry.ID)
		return
	}
	s.Hub.Broadcast(roomID, revision, []byte(message))
}

// pause logs a failure and waits before the next try. It returns false if
// ctx ended first.
func (s *StreamReader) pause(ctx context.Context, delay time.Duration, what string, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	s.Logger.Error("theater: could not "+what+"; retrying", "error", err)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}
