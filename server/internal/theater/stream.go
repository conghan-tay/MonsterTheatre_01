package theater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"sync"
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
	// readerFailingAfter is how long reads may fail before the reader is
	// unhealthy: a blip is retried quietly.
	readerFailingAfter = 10 * time.Second
	// readerStuckAfter is how long the reader may go without finishing a
	// read before it is unhealthy. A read waits at most streamBlock, so
	// this means the loop has stopped or is stuck.
	readerStuckAfter = 15 * time.Second
	// failureLogEvery is how often a continuing failure is logged again.
	failureLogEvery = 30 * time.Second
)

// StreamClient is the part of Redis the reader uses. *redis.Client
// implements it.
type StreamClient interface {
	XRevRangeN(ctx context.Context, stream, start, stop string, count int64) *redis.XMessageSliceCmd
	XRead(ctx context.Context, a *redis.XReadArgs) *redis.XStreamSliceCmd
}

// Broadcaster delivers an update to a room's connections and says how many
// it was queued for and how many were dropped for falling behind. *Hub
// implements it.
type Broadcaster interface {
	Broadcast(roomID string, revision int, msg []byte) (connections, dropped int)
}

// UpdateWatcher is told of every update read. *StallWatcher implements it.
type UpdateWatcher interface {
	Seen(roomID string, revision int, state performance.State)
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
	// Stalls, if set, is told of every update read.
	Stalls UpdateWatcher
	// Now defaults to time.Now.
	Now func() time.Time

	// progress is what Healthy reports on, and what the reader's own log
	// lines say (docs/decisions.md, D061).
	mu           sync.Mutex
	running      bool
	lastProgress time.Time
	failingSince time.Time
	failures     int
	lastFailLog  time.Time
	lastIDs      map[string]string
}

func (s *StreamReader) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// started marks the reader as running.
func (s *StreamReader) started(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running, s.lastProgress = true, now
}

// readOK records a read that finished, with or without entries, and logs
// the end of any failure before it.
func (s *StreamReader) readOK(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures > 0 {
		s.Logger.Info("stream reader recovered",
			"downtime_s", int(now.Sub(s.failingSince).Seconds()), "failures", s.failures, "last_ids", s.lastIDs)
	}
	s.lastProgress, s.failingSince, s.failures = now, time.Time{}, 0
}

// readFailed records a failed call to Redis. It is logged when the failure
// begins and then every failureLogEvery while it goes on, not on every
// retry.
func (s *StreamReader) readFailed(now time.Time, what string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastProgress = now
	if s.failures == 0 {
		s.failingSince = now
	}
	s.failures++
	if s.failures == 1 || now.Sub(s.lastFailLog) >= failureLogEvery {
		s.lastFailLog = now
		s.Logger.Error("stream read failed",
			"op", what, "failures", s.failures, "failing_for_s", int(now.Sub(s.failingSince).Seconds()),
			"last_ids", s.lastIDs, "error", err.Error())
	}
}

// noteIDs records where each stream has been read up to, for the log.
func (s *StreamReader) noteIDs(ids map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastIDs = maps.Clone(ids)
}

// Healthy reports whether the reader is keeping up: running, finishing
// reads, and not failing for longer than a blip. /healthz asks it.
func (s *StreamReader) Healthy(now time.Time) error {
	return s.status(now, readerFailingAfter)
}

// Reading is Healthy without the allowance for a blip: whether updates are
// getting through at this moment. The stall watcher asks it, since a stall
// seen during a blip is still one this server could not have seen the end
// of.
func (s *StreamReader) Reading(now time.Time) error {
	return s.status(now, 0)
}

func (s *StreamReader) status(now time.Time, allowance time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.running:
		return errors.New("the stream reader is not running")
	case s.failures > 0 && now.Sub(s.failingSince) >= allowance:
		return fmt.Errorf("stream reads failing for %ds", int(now.Sub(s.failingSince).Seconds()))
	case now.Sub(s.lastProgress) > readerStuckAfter:
		return fmt.Errorf("no read has finished for %ds", int(now.Sub(s.lastProgress).Seconds()))
	}
	return nil
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
	s.started(s.now())
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

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
				s.readOK(s.now())
				lastID[key] = "0-0" // an empty stream: everything in it will be new
			default:
				s.readOK(s.now())
				lastID[key] = newest[0].ID
			}
		}
	}
	s.noteIDs(lastID)

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
			s.readOK(s.now())
			continue // nothing new within the block time
		}
		if err != nil {
			if !s.pause(ctx, delay, "read the streams", err) {
				return
			}
			continue
		}
		s.readOK(s.now())
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
		s.noteIDs(lastID)
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
	connections, dropped := s.Hub.Broadcast(roomID, revision, []byte(message))

	// The phase is read only for the log and the stall watcher; the
	// message itself is the browsers' business.
	var update performance.Message
	readable := json.Unmarshal([]byte(message), &update) == nil
	s.Logger.Info("update broadcast",
		"room_id", roomID, "performance_id", update.State.PerformanceID, "revision", revision,
		"phase", update.State.Phase, "stream_id", entry.ID, "connections", connections, "dropped", dropped)
	if readable && s.Stalls != nil {
		s.Stalls.Seen(roomID, revision, update.State)
	}
}

// pause logs a failure and waits before the next try. It returns false if
// ctx ended first.
func (s *StreamReader) pause(ctx context.Context, delay time.Duration, what string, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	s.readFailed(s.now(), what, err)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}
