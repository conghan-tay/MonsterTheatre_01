package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
)

// These tests check the harness itself. If they pass and another test fails,
// the failure is in the application, not in the test stack.

func TestStackIsHealthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.New(stack.BaseURL).Get(ctx, "/healthz")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "body: %s", resp.Body)

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	require.NoError(t, resp.Decode(&body))
	require.Equal(t, "ok", body.Status)
	require.Equal(t, map[string]string{"database": "ok", "redis": "ok", "temporal": "ok"}, body.Checks)
}

func TestWorkerCanBeStoppedAndRestarted(t *testing.T) {
	require.True(t, stack.WorkerRunning())

	require.NoError(t, stack.StopWorker())
	require.False(t, stack.WorkerRunning())
	require.Error(t, stack.StopWorker(), "stopping twice is a test bug and is reported")

	require.NoError(t, stack.StartWorker())
	require.True(t, stack.WorkerRunning())
	require.Error(t, stack.StartWorker(), "starting twice is a test bug and is reported")
}
