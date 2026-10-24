package httpapi

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
)

type requestIDKey struct{}

// RequestID is the ID of the request ctx belongs to, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// safeRequestID is what a caller's own X-Request-Id must look like to be
// kept: short, and nothing that could break a log line.
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Logged logs one "http request" line per request, with its route pattern,
// status, duration, and an ID that is also returned in X-Request-Id and
// available to handlers through RequestID (docs/decisions.md, D060). The
// liveness check is logged at debug only. A WebSocket's line is written
// when it closes, so its duration is the connection's.
func Logged(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))

		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if r.Pattern == "GET /livez" {
			level = slog.LevelDebug
		}
		fields := []any{
			"request_id", id, "method", r.Method, "route", r.Pattern,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds(),
		}
		if room := r.PathValue("roomId"); room != "" {
			fields = append(fields, "room_id", room)
		}
		logger.Log(r.Context(), level, "http request", fields...)
	})
}

// statusRecorder remembers the status written, and still lets a handler
// take over the connection for a WebSocket.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Hijack hands the connection to a WebSocket; the request then counts as
// switched protocols.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("the connection cannot be taken over")
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		s.status, s.wroteHeader = http.StatusSwitchingProtocols, true
	}
	return conn, rw, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
