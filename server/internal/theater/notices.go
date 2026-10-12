package theater

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// A notice is something one server has to tell the others, so that every
// browser in a room hears of it whichever server its connection is on.
//
// The worker's updates already travel this way: it appends them to a Redis
// stream that every server reads (docs/decisions.md, D024). Notices are the
// same thing for news that starts in a server, on a stream of their own. A
// server that has news appends a notice and does nothing else; it hears
// its own notice from the stream like every other server, and each one
// then deals with the connections it holds (docs/decisions.md, D046).
const (
	// NoticeStreamKey is the Redis stream notices are appended to.
	NoticeStreamKey = "room-notices"

	noticeFieldRoomID  = "room_id"
	noticeFieldKind    = "kind"
	noticeFieldMessage = "message"
	noticeFieldExcept  = "except"

	// noticeAudienceChanged says that someone joined a room's audience or
	// changed their name. It carries nothing more: each server that has
	// the room's director connected reads the names itself, which is
	// always at least as new as the notice. If the names travelled in the
	// notice, two joins handled by two servers could be appended in the
	// wrong order and leave the director looking at the older list.
	noticeAudienceChanged = "audience_changed"
	// noticeAnnounce carries a message for everyone in a room: the failed
	// snapshot of a run found dead. It names the connection that found
	// out, which has been answered already.
	noticeAnnounce = "announce"

	// noticeStreamMaxLen is roughly how many notices the stream keeps.
	// Nothing reads history: a connecting director is sent the audience,
	// and a connecting browser its snapshot.
	noticeStreamMaxLen = 1000
	// noticeTimeout bounds acting on a notice that needs the database.
	noticeTimeout = 5 * time.Second
)

// NoticeAdder appends to a Redis stream. *redis.Client implements it.
type NoticeAdder interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
}

// notice is one entry of the notice stream.
type notice struct {
	roomID string
	kind   string
	// message and except are set on an announcement.
	message string
	except  string
}

// publish appends a notice for every server, this one included.
//
// If it cannot be appended, this server at least acts on it for its own
// connections, which is everyone when there is one server. The same goes
// for a service given no stream at all.
func (s *Service) publish(ctx context.Context, n notice) {
	if s.Notices != nil {
		err := s.Notices.XAdd(ctx, &redis.XAddArgs{
			Stream: NoticeStreamKey,
			MaxLen: noticeStreamMaxLen,
			Approx: true,
			Values: map[string]any{
				noticeFieldRoomID:  n.roomID,
				noticeFieldKind:    n.kind,
				noticeFieldMessage: n.message,
				noticeFieldExcept:  n.except,
			},
		}).Err()
		if err == nil {
			return
		}
		if ctx.Err() == nil {
			s.Logger.Warn("theater: could not publish a notice; telling this server's connections only",
				"room_id", n.roomID, "kind", n.kind, "error", err)
		}
	}
	s.act(ctx, n)
}

// HandleNotice acts on one entry read from the notice stream. The stream
// reader calls it, on every server, for every notice. One that cannot be
// read is logged and skipped.
func (s *Service) HandleNotice(ctx context.Context, entry redis.XMessage) {
	text := func(field string) string {
		value, _ := entry.Values[field].(string)
		return value
	}
	n := notice{
		roomID:  text(noticeFieldRoomID),
		kind:    text(noticeFieldKind),
		message: text(noticeFieldMessage),
		except:  text(noticeFieldExcept),
	}
	known := n.kind == noticeAudienceChanged || (n.kind == noticeAnnounce && n.message != "")
	if n.roomID == "" || !known {
		s.Logger.Error("theater: malformed notice in stream", "stream_id", entry.ID)
		return
	}
	s.act(ctx, n)
}

// act does what a notice asks, for the connections this server holds.
func (s *Service) act(ctx context.Context, n notice) {
	switch n.kind {
	case noticeAudienceChanged:
		// Most servers have nobody to tell about most rooms.
		if !s.Hub.hasDirector(n.roomID) {
			return
		}
		// Off the caller's goroutine: this reads the database, and the
		// caller is usually the stream reader, which must not keep every
		// room's updates waiting on one room's query. Two of these at once
		// are safe: tellAudience reads and queues as one step.
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noticeTimeout)
			defer cancel()
			s.tellAudience(ctx, n.roomID, nil)
		}()
	case noticeAnnounce:
		s.Hub.announce(n.roomID, n.except, []byte(n.message))
	}
}
