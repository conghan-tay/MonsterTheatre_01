package theater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

// noticeLog stands in for the notice stream on the writing side: it keeps
// what was appended, or refuses.
type noticeLog struct {
	mu      sync.Mutex
	err     error
	entries []redis.XMessage
	streams []string
}

func (n *noticeLog) XAdd(_ context.Context, a *redis.XAddArgs) *redis.StringCmd {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return redis.NewStringResult("", n.err)
	}
	id := strconv.Itoa(len(n.entries)+1) + "-0"
	n.entries = append(n.entries, redis.XMessage{ID: id, Values: a.Values.(map[string]any)})
	n.streams = append(n.streams, a.Stream)
	return redis.NewStringResult(id, nil)
}

func (n *noticeLog) all() []redis.XMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]redis.XMessage(nil), n.entries...)
}

// A server with news appends a notice and tells nobody itself. Its own
// connections hear when the notice comes back from the stream, as every
// other server's do.
func TestAnAudienceChangeIsPublishedAndActedOnWhenItComesBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	members, notices := &fakeMembers{}, &noticeLog{}
	svc.Rooms, svc.Members, svc.Notices = doorByCookie{}, members, notices

	director := dialAs(t, ctx, url, true)
	require.Equal(t, "snapshot", readRaw(t, ctx, director)["type"])
	require.Equal(t, 0.0, readRaw(t, ctx, director)["count"], "the audience as it is on connecting")

	members.set("Ana")
	svc.AudienceChanged(ctx, "room")
	published := notices.all()
	require.Len(t, published, 1)
	require.Equal(t, []string{NoticeStreamKey}, notices.streams)
	require.Equal(t, map[string]any{"room_id": "room", "kind": "audience_changed", "message": "", "except": ""},
		published[0].Values, "an event and nothing more: no names travel in the notice")

	// Nothing has been sent yet: the next thing the director gets is the
	// audience, and only once the notice has come back.
	svc.HandleNotice(ctx, published[0])
	require.Equal(t, map[string]any{"type": "audience", "count": 1.0, "nicknames": []any{"Ana"}}, readRaw(t, ctx, director))
}

// Every server is handed every notice, but only one that holds the room's
// director reads the names.
func TestAnAudienceNoticeForARoomWithNoDirectorHereIsLeftAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	members := &fakeMembers{nicknames: []string{"Ana"}}
	svc.Rooms, svc.Members = doorByCookie{}, members

	member := dialAs(t, ctx, url, false)
	require.Equal(t, "snapshot", readRaw(t, ctx, member)["type"])

	changed := redis.XMessage{ID: "1-0", Values: map[string]any{"room_id": "room", "kind": "audience_changed"}}
	svc.HandleNotice(ctx, changed)
	svc.HandleNotice(ctx, redis.XMessage{ID: "2-0", Values: map[string]any{"room_id": "another-room", "kind": "audience_changed"}})
	svc.Hub.Broadcast("room", 1, updateJSON(1))
	require.Equal(t, "update", readRaw(t, ctx, member)["type"], "the audience member is sent nothing about the audience")
	require.Zero(t, members.asked(), "and the database is not read for it")
}

func TestANoticeThatCannotBePublishedIsStillActedOnHere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	members := &fakeMembers{}
	svc.Rooms, svc.Members = doorByCookie{}, members
	svc.Notices = &noticeLog{err: errors.New("connection refused")}

	director := dialAs(t, ctx, url, true)
	readRaw(t, ctx, director)
	readRaw(t, ctx, director)

	members.set("Ana")
	svc.AudienceChanged(ctx, "room")
	require.Equal(t, []any{"Ana"}, readRaw(t, ctx, director)["nicknames"], "Redis is down, and this server's director is told all the same")
}

// A run found dead is announced by notice. The connection that found out
// is named in it and is not told twice, here or on any other server.
func TestADeadRunIsAnnouncedByNotice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	rows, notices := runningRow(), &noticeLog{}
	svc.Performances, svc.Failures, svc.Notices = rows, &fakeFailer{rows: rows}, notices
	svc.Workflows = &scriptedWorkflow{answer: func(call int) (performance.Snapshot, error) {
		if call == 1 {
			return votingAtTwo, nil
		}
		return performance.Snapshot{}, performance.ErrWorkflowClosed
	}}

	waiting := dial(t, ctx, url)
	require.Equal(t, 2, readMessage(t, ctx, waiting).Revision)
	late := dial(t, ctx, url)
	require.Equal(t, performance.PhaseFailed, readMessage(t, ctx, late).State.Phase, "the browser that found out is answered directly")

	published := notices.all()
	require.Len(t, published, 1)
	require.Equal(t, "announce", published[0].Values["kind"])
	require.Equal(t, "room", published[0].Values["room_id"])
	require.Contains(t, published[0].Values["message"], `"phase":"failed"`)
	require.NotEmpty(t, published[0].Values["except"], "it names the connection already answered")

	svc.HandleNotice(ctx, published[0])
	told := readMessage(t, ctx, waiting)
	require.Equal(t, "snapshot", told.Type)
	require.Equal(t, performance.PhaseFailed, told.State.Phase)

	// The one that found out is passed over: its next message is this one.
	svc.Hub.Broadcast("room", 9, updateJSON(9))
	next := readMessage(t, ctx, late)
	require.Equal(t, "update", next.Type)

	// On a server that does not hold that connection, everyone is told.
	fromElsewhere := published[0]
	fromElsewhere.Values = map[string]any{"room_id": "room", "kind": "announce", "message": published[0].Values["message"], "except": "a-connection-on-another-server"}
	svc.HandleNotice(ctx, fromElsewhere)
	require.Equal(t, "snapshot", readMessage(t, ctx, late).Type)
}

func TestANoticeThatCannotBeReadIsSkipped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)

	for _, values := range []map[string]any{
		{},
		{"kind": "audience_changed"},            // no room
		{"room_id": "room", "kind": "applause"}, // a kind this server does not know
		{"room_id": "room", "kind": "announce"}, // nothing to announce
		{"room_id": 7, "kind": []string{"announce"}}, // not text at all
	} {
		svc.HandleNotice(ctx, redis.XMessage{ID: "1-0", Values: values})
	}
	svc.Hub.Broadcast("room", 1, updateJSON(1))
	require.Equal(t, "update", readMessage(t, ctx, ws).Type, "none of those sent anything")
}

// twoStreams is the update stream and the notice stream, held in memory.
// Entry IDs are "1-0", "2-0", ... within each.
type twoStreams struct {
	mu       sync.Mutex
	entries  map[string][]redis.XMessage
	failures int
	reads    int
}

func (f *twoStreams) add(stream string, values map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string][]redis.XMessage{}
	}
	f.entries[stream] = append(f.entries[stream], redis.XMessage{ID: strconv.Itoa(len(f.entries[stream])+1) + "-0", Values: values})
}

func (f *twoStreams) update(revision int) {
	f.add(performance.StreamKey, map[string]any{
		performance.StreamFieldRoomID: "room", performance.StreamFieldRevision: strconv.Itoa(revision), performance.StreamFieldMessage: fmt.Sprint("update ", revision),
	})
}

func (f *twoStreams) notice(label string) {
	f.add(NoticeStreamKey, map[string]any{"room_id": "room", "kind": "audience_changed", "label": label})
}

func (f *twoStreams) fail() bool {
	if f.failures > 0 {
		f.failures--
		return true
	}
	return false
}

func (f *twoStreams) XRevRangeN(_ context.Context, stream, _, _ string, _ int64) *redis.XMessageSliceCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail() {
		return redis.NewXMessageSliceCmdResult(nil, errors.New("connection refused"))
	}
	all := f.entries[stream]
	if len(all) == 0 {
		return redis.NewXMessageSliceCmdResult(nil, nil)
	}
	return redis.NewXMessageSliceCmdResult(all[len(all)-1:], nil)
}

func (f *twoStreams) XRead(ctx context.Context, a *redis.XReadArgs) *redis.XStreamSliceCmd {
	f.mu.Lock()
	if f.fail() {
		f.mu.Unlock()
		return redis.NewXStreamSliceCmdResult(nil, errors.New("connection reset"))
	}
	f.reads++
	n := len(a.Streams) / 2 // the names, then a position for each
	var out []redis.XStream
	for i := range n {
		name := a.Streams[i]
		after, _ := strconv.Atoi(strings.TrimSuffix(a.Streams[n+i], "-0"))
		all := f.entries[name]
		if newer := all[min(after, len(all)):]; len(newer) > 0 {
			out = append(out, redis.XStream{Stream: name, Messages: append([]redis.XMessage(nil), newer...)})
		}
	}
	f.mu.Unlock()
	if len(out) == 0 {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Millisecond):
		}
		return redis.NewXStreamSliceCmdResult(nil, redis.Nil)
	}
	return redis.NewXStreamSliceCmdResult(out, nil)
}

type recordingNotices struct {
	mu     sync.Mutex
	labels []string
}

func (r *recordingNotices) HandleNotice(_ context.Context, entry redis.XMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels = append(r.labels, entry.Values["label"].(string))
}

func (r *recordingNotices) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.labels...)
}

func TestStreamReaderFollowsBothStreamsAndResumesEachAfterRedisFails(t *testing.T) {
	streams := &twoStreams{failures: 2} // Redis is down when the server starts
	streams.update(1)                   // already there: not wanted
	streams.notice("old")
	hub, notices := &recordingHub{}, &recordingNotices{}
	reader := &StreamReader{
		Redis: streams, Hub: hub, Notices: notices,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RetryDelay: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reader.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		streams.mu.Lock()
		defer streams.mu.Unlock()
		return streams.reads > 0
	}, time.Second, time.Millisecond)

	streams.update(2)
	streams.notice("first")
	require.Eventually(t, func() bool { return len(hub.all()) == 1 && len(notices.all()) == 1 }, time.Second, time.Millisecond)

	// Redis fails again; entries arrive on both streams meanwhile, unevenly.
	streams.mu.Lock()
	streams.failures = 3
	streams.mu.Unlock()
	streams.notice("second")
	streams.update(3)
	streams.notice("third")

	require.Eventually(t, func() bool { return len(hub.all()) == 2 && len(notices.all()) == 3 }, time.Second, time.Millisecond)
	require.Equal(t, []broadcast{{"room", 2, "update 2"}, {"room", 3, "update 3"}}, hub.all(),
		"updates: nothing from before the start, nothing lost or repeated")
	require.Equal(t, []string{"first", "second", "third"}, notices.all(),
		"notices: each stream is resumed from its own place")
}
