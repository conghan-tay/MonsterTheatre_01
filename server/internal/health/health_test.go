package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, checks ...Check) (int, response) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	Handler(logger, checks...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var body response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec.Code, body
}

func TestHandlerAllChecksPass(t *testing.T) {
	ok := func(context.Context) error { return nil }

	code, body := serve(t, Check{"database", ok}, Check{"redis", ok})

	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "ok", body.Status)
	require.Equal(t, map[string]string{"database": "ok", "redis": "ok"}, body.Checks)
}

func TestHandlerNamesFailedCheckWithoutLeakingError(t *testing.T) {
	ok := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("dial tcp 10.0.0.5:6379: refused") }

	code, body := serve(t, Check{"database", ok}, Check{"redis", down})

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, "unavailable", body.Status)
	require.Equal(t, map[string]string{"database": "ok", "redis": "unreachable"}, body.Checks)
}
