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
