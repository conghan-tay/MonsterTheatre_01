// Package health serves the /healthz endpoint.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

const checkTimeout = 2 * time.Second

// Check reports whether one dependency is reachable.
type Check struct {
	Name string
	Run  func(ctx context.Context) error
}

type response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Handler runs every check and answers 200 when all pass and 503 otherwise.
// The response names which dependency failed; the underlying error is logged
// and not returned, since the endpoint is unauthenticated.
func Handler(logger *slog.Logger, checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := response{Status: "ok", Checks: make(map[string]string, len(checks))}
		for _, check := range checks {
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			err := check.Run(ctx)
			cancel()
			if err != nil {
				logger.Warn("health check failed", "check", check.Name, "error", err)
				resp.Status = "unavailable"
				resp.Checks[check.Name] = "unreachable"
				continue
			}
			resp.Checks[check.Name] = "ok"
		}

		code := http.StatusOK
		if resp.Status != "ok" {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// Live answers 200 whenever the process is serving requests. It is what the
// load balancer asks. The dependency checks are deliberately not part of it:
// were they, one dependency being briefly unreachable would have the load
// balancer take the server out of service and close every WebSocket.
func Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}
