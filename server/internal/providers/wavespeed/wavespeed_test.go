package wavespeed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// serve runs a stand-in for the WaveSpeed API and returns a client pointed
// at it, and the stand-in's own address.
func serve(t *testing.T, handler http.HandlerFunc) (*LipSync, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &LipSync{APIKey: "test-key", BaseURL: srv.URL}, srv.URL
}

func reply(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success", "data": data}))
}

func TestUploadAsksForATicketThenPutsTheFileWithoutTheKey(t *testing.T) {
	var ticket map[string]any
	var put *http.Request
	var putBody []byte
	// The ticket points the upload back at the stand-in, at an address only
	// known once it is running.
	var base string
	l, addr := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/media/uploads":
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &ticket))
			reply(t, w, map[string]any{
				"download_url": "https://files.example/portrait.png",
				"upload": map[string]any{
					"method":  "PUT",
					"url":     base + "/signed/portrait.png?sig=abc",
					"headers": map[string]string{"Content-Type": "image/png", "If-None-Match": "*"},
				},
			})
		case "/signed/portrait.png":
			put = r.Clone(context.Background())
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	})
	base = addr

	url, err := l.Upload(context.Background(), providers.UploadRequest{
		Filename: "portrait.png", ContentType: "image/png", Data: []byte("png bytes"),
	})
	require.NoError(t, err)
	require.Equal(t, "https://files.example/portrait.png", url)

	require.Equal(t, map[string]any{"filename": "portrait.png", "size": float64(9), "content_type": "image/png"}, ticket)
	require.NotNil(t, put, "the file was put")
	require.Equal(t, http.MethodPut, put.Method)
	require.Equal(t, "sig=abc", put.URL.RawQuery)
	require.Equal(t, "image/png", put.Header.Get("Content-Type"))
	require.Equal(t, "*", put.Header.Get("If-None-Match"), "the ticket's headers are sent as given")
	require.Empty(t, put.Header.Get("Authorization"), "the API key never goes to the storage host")
	require.Equal(t, []byte("png bytes"), putBody)
}

func TestSubmitSendsThePortraitTheRecordingAndTheResolution(t *testing.T) {
	var seen *http.Request
	var body map[string]any
	l, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		reply(t, w, map[string]any{"id": "pred_123", "status": "created"})
	})

	id, err := l.Submit(context.Background(), providers.LipSyncRequest{
		RoomID: "room-1", Turn: 3, ImageURL: "https://files.example/a.png", AudioURL: "https://files.example/a.mp3", Resolution: "720p",
	})
	require.NoError(t, err)
	require.Equal(t, "pred_123", id)
	require.Equal(t, http.MethodPost, seen.Method)
	require.Equal(t, "/api/v3/wavespeed-ai/infinitetalk", seen.URL.Path)
	require.Equal(t, "Bearer test-key", seen.Header.Get("Authorization"))
	require.Equal(t, "application/json", seen.Header.Get("Content-Type"))
	require.Equal(t, map[string]any{
		"image": "https://files.example/a.png", "audio": "https://files.example/a.mp3", "resolution": "720p",
	}, body, "the two inputs and the resolution; nothing else")
}

func TestSubmitWithoutAnIDIsAnError(t *testing.T) {
	l, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		reply(t, w, map[string]any{"status": "created"})
	})
	_, err := l.Submit(context.Background(), providers.LipSyncRequest{})
	require.ErrorContains(t, err, "no id")
}

func TestResultMapsEveryStatus(t *testing.T) {
	for status, want := range map[string]providers.LipSyncResult{
		"created":    {Status: providers.LipSyncPending},
		"processing": {Status: providers.LipSyncPending},
		"completed":  {Status: providers.LipSyncCompleted, VideoURL: "https://files.example/out.mp4"},
		"failed":     {Status: providers.LipSyncFailed, Error: "face not found"},
		"cancelled":  {Status: providers.LipSyncFailed, Error: "face not found"},
		"timeout":    {Status: providers.LipSyncFailed, Error: "face not found"},
		"deleted":    {Status: providers.LipSyncFailed, Error: "face not found"},
	} {
		t.Run(status, func(t *testing.T) {
			var seen *http.Request
			l, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				seen = r.Clone(context.Background())
				data := map[string]any{"id": "pred_123", "status": status}
				if status == "completed" {
					data["outputs"] = []string{"https://files.example/out.mp4"}
				} else if want.Status == providers.LipSyncFailed {
					data["error"] = "face not found"
				}
				reply(t, w, data)
			})
			got, err := l.Result(context.Background(), "pred_123")
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Equal(t, http.MethodGet, seen.Method)
			require.Equal(t, "/api/v3/predictions/pred_123/result", seen.URL.Path)
			require.Equal(t, "Bearer test-key", seen.Header.Get("Authorization"))
		})
	}
}

func TestACompletedResultWithNoVideoIsAFailure(t *testing.T) {
	l, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		reply(t, w, map[string]any{"id": "pred_123", "status": "completed", "outputs": []string{}})
	})
	got, err := l.Result(context.Background(), "pred_123")
	require.NoError(t, err)
	require.Equal(t, providers.LipSyncFailed, got.Status)
}

func TestErrorsWorthRetryingAndErrorsNotWorthRetrying(t *testing.T) {
	for status, permanent := range map[int]bool{
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
		http.StatusServiceUnavailable:  false,
		http.StatusGatewayTimeout:      false,
		http.StatusUnauthorized:        true,
		http.StatusNotFound:            true,
		http.StatusBadRequest:          true,
	} {
		l, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"code":0,"message":"no"}`, status)
		})
		_, err := l.Result(context.Background(), "pred_123")
		require.Error(t, err, "status %d", status)
		require.Equal(t, permanent, providers.IsPermanent(err), "status %d: %v", status, err)
	}
}

func TestDownloadFetchesTheVideoWithoutTheKey(t *testing.T) {
	var seen *http.Request
	_, base := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("mp4 bytes"))
	})
	l := &LipSync{APIKey: "test-key", BaseURL: "https://unused.example"}

	video, err := l.Download(context.Background(), base+"/outputs/out.mp4")
	require.NoError(t, err)
	require.Equal(t, []byte("mp4 bytes"), video)
	require.Equal(t, "/outputs/out.mp4", seen.URL.Path)
	require.Empty(t, seen.Header.Get("Authorization"), "the API key never goes to the storage host")
}

func TestDownloadOfNothingIsAnError(t *testing.T) {
	_, base := serve(t, func(w http.ResponseWriter, r *http.Request) {})
	l := &LipSync{APIKey: "test-key"}
	_, err := l.Download(context.Background(), base+"/out.mp4")
	require.Error(t, err)
}
