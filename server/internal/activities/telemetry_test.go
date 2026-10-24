package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/telemetry"
)

type addedStream struct {
	id  string
	err error
}

func (s addedStream) XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd {
	return redis.NewStringResult(s.id, s.err)
}

func TestPublishLogsTheRevisionAndTheStreamIDItWasGiven(t *testing.T) {
	var buf bytes.Buffer
	a := &Activities{Stream: addedStream{id: "1712-0"}, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	update := performance.Update{RoomID: "room-1", Revision: 7, State: performance.State{Phase: "voting", Turn: 3}}
	require.NoError(t, a.Publish(context.Background(), update))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "update published", line["msg"])
	require.Equal(t, "room-1", line["room_id"])
	require.EqualValues(t, 7, line["revision"])
	require.Equal(t, "voting", line["phase"])
	require.Equal(t, "1712-0", line["stream_id"], "the ID the server's reader will see")
}

func TestAFailedPublishIsNotLoggedAsPublished(t *testing.T) {
	var buf bytes.Buffer
	a := &Activities{Stream: addedStream{err: errors.New("connection refused")}, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	require.Error(t, a.Publish(context.Background(), performance.Update{RoomID: "room-1", Revision: 7}))
	require.NotContains(t, buf.String(), "update published", "the attempt's own line says it failed")
}

func TestEveryActivityInputNamesItsRoomPerformanceAndTurn(t *testing.T) {
	has := func(f telemetry.LogFielder, want ...any) {
		t.Helper()
		got := f.LogFields()
		for i := 0; i < len(want); i += 2 {
			idx := -1
			for j := 0; j+1 < len(got); j += 2 {
				if got[j] == want[i] {
					idx = j
				}
			}
			require.GreaterOrEqual(t, idx, 0, "%T has no %v", f, want[i])
			require.Equal(t, want[i+1], got[idx+1], "%T %v", f, want[i])
		}
	}
	has(ChoicesInput{RoomID: "r", Turn: 2}, "room_id", "r", "turn", 2)
	has(TagsInput{RoomID: "r", Turn: 2}, "room_id", "r", "turn", 2)
	has(SpeechInput{RoomID: "r", PerformanceID: "p", Turn: 2}, "room_id", "r", "performance_id", "p", "turn", 2)
	has(LipSyncInputs{RoomID: "r", PerformanceID: "p", Turn: 2}, "room_id", "r", "performance_id", "p", "turn", 2)
	has(LipSyncSubmission{RoomID: "r", Turn: 2}, "room_id", "r", "turn", 2)
	has(LipSyncWait{RoomID: "r", PerformanceID: "p", Turn: 2}, "room_id", "r", "performance_id", "p", "turn", 2)
	has(CompleteInput{PerformanceID: "p"}, "performance_id", "p")
	has(FailInput{PerformanceID: "p"}, "performance_id", "p")
	has(performance.Update{RoomID: "r", Revision: 4, State: performance.State{Phase: "voting", Turn: 2}}, "room_id", "r", "revision", 4, "phase", "voting", "turn", 2)
}
