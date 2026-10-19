// Package wavespeed is the lip-sync adapter backed by WaveSpeed's
// InfiniteTalk model. It makes plain HTTP requests, so no SDK is used.
//
// InfiniteTalk reads its portrait and recording from URLs. Ours are private,
// so each is first uploaded to WaveSpeed's own file store, which keeps it for
// seven days. A video is submitted once and its result asked for until it is
// done; it is then downloaded, because WaveSpeed keeps outputs only for a few
// days (https://wavespeed.ai/docs/upload-files-api,
// https://wavespeed.ai/docs/get-result).
package wavespeed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

const (
	// DefaultBaseURL is the WaveSpeed API.
	DefaultBaseURL = "https://api.wavespeed.ai"
	// model is the InfiniteTalk endpoint, under /api/v3/.
	model = "wavespeed-ai/infinitetalk"

	// maxVideoBytes bounds one video. A 480p clip of 15 seconds is a few
	// megabytes; 720p perhaps twice that.
	maxVideoBytes = 100 << 20
	// maxReplyBytes bounds a JSON answer.
	maxReplyBytes = 1 << 20
	// requestTimeout bounds one request, upload and download included.
	requestTimeout = 2 * time.Minute
)

// LipSync is a providers.LipSync that asks WaveSpeed.
type LipSync struct {
	// APIKey authenticates requests to the API. It is never sent anywhere
	// else: uploads go to a signed URL, and downloads to a public one.
	APIKey string
	// BaseURL and HTTP default to the real API and a client with a timeout.
	// Tests point them at a local server.
	BaseURL string
	HTTP    *http.Client
}

var _ providers.LipSync = (*LipSync)(nil)

// envelope is the shape of every WaveSpeed answer.
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (l *LipSync) client() *http.Client {
	if l.HTTP != nil {
		return l.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

func (l *LipSync) api(path string) string {
	base := l.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return strings.TrimRight(base, "/") + path
}

// call makes one request to the API and decodes the data of its answer.
func (l *LipSync) call(ctx context.Context, what, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return providers.Permanent(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.api(path), reader)
	if err != nil {
		return providers.Permanent(err)
	}
	req.Header.Set("Authorization", "Bearer "+l.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := l.client().Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	if err := statusError(what, resp); err != nil {
		return err
	}
	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReplyBytes)).Decode(&env); err != nil {
		return fmt.Errorf("%s: read answer: %w", what, err)
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		return fmt.Errorf("%s: read answer: %w", what, err)
	}
	return nil
}

// statusError turns an unsuccessful answer into an error: one not worth
// retrying for a request that is itself wrong (a bad key, an unknown
// prediction), and a plain one for 408, 429, and server errors.
func statusError(what string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// The start of the body says what was wrong. It never contains the key.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	err := fmt.Errorf("%s: status %d: %s", what, resp.StatusCode, strings.TrimSpace(string(detail)))
	if providers.PermanentStatus(resp.StatusCode) {
		return providers.Permanent(err)
	}
	return err
}

type uploadTicket struct {
	DownloadURL string `json:"download_url"`
	Upload      struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	} `json:"upload"`
}

// Upload stores one input file with WaveSpeed and returns the URL the model
// reads it from. It asks for an upload ticket, then sends the bytes to the
// signed URL in it.
func (l *LipSync) Upload(ctx context.Context, req providers.UploadRequest) (string, error) {
	var ticket uploadTicket
	err := l.call(ctx, "upload ticket", http.MethodPost, "/api/v3/media/uploads", map[string]any{
		"filename": req.Filename, "size": len(req.Data), "content_type": req.ContentType,
	}, &ticket)
	if err != nil {
		return "", err
	}
	if ticket.DownloadURL == "" || ticket.Upload.URL == "" {
		return "", errors.New("upload ticket: the answer had no upload or download URL")
	}
	method := ticket.Upload.Method
	if method == "" {
		method = http.MethodPut
	}
	put, err := http.NewRequestWithContext(ctx, method, ticket.Upload.URL, bytes.NewReader(req.Data))
	if err != nil {
		return "", providers.Permanent(err)
	}
	put.ContentLength = int64(len(req.Data))
	for name, value := range ticket.Upload.Headers {
		put.Header.Set(name, value)
	}
	resp, err := l.client().Do(put)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if err := statusError("upload", resp); err != nil {
		return "", err
	}
	return ticket.DownloadURL, nil
}

// Submit starts one InfiniteTalk video and returns its prediction ID.
// It must not be repeated automatically (providers.LipSync).
func (l *LipSync) Submit(ctx context.Context, req providers.LipSyncRequest) (string, error) {
	var task struct {
		ID string `json:"id"`
	}
	err := l.call(ctx, "submit lip sync", http.MethodPost, "/api/v3/"+model, map[string]any{
		"image": req.ImageURL, "audio": req.AudioURL, "resolution": req.Resolution,
	}, &task)
	if err != nil {
		return "", err
	}
	if task.ID == "" {
		return "", errors.New("submit lip sync: the answer had no id")
	}
	return task.ID, nil
}

// Result asks where a prediction stands. WaveSpeed's statuses are mapped to
// three: still going, done (with the video's URL), or ended without a video.
func (l *LipSync) Result(ctx context.Context, id string) (providers.LipSyncResult, error) {
	var result struct {
		Status  string   `json:"status"`
		Outputs []string `json:"outputs"`
		Error   string   `json:"error"`
	}
	err := l.call(ctx, "lip sync result", http.MethodGet, "/api/v3/predictions/"+url.PathEscape(id)+"/result", nil, &result)
	if err != nil {
		return providers.LipSyncResult{}, err
	}
	switch result.Status {
	case "completed":
		if len(result.Outputs) == 0 || result.Outputs[0] == "" {
			return providers.LipSyncResult{Status: providers.LipSyncFailed, Error: "completed without a video"}, nil
		}
		return providers.LipSyncResult{Status: providers.LipSyncCompleted, VideoURL: result.Outputs[0]}, nil
	case "failed", "cancelled", "timeout", "deleted":
		reason := result.Error
		if reason == "" {
			reason = result.Status
		}
		return providers.LipSyncResult{Status: providers.LipSyncFailed, Error: reason}, nil
	default:
		// created, processing, or anything new: not finished.
		return providers.LipSyncResult{Status: providers.LipSyncPending}, nil
	}
}

// Download fetches a finished video. The URL is a public one on WaveSpeed's
// storage, so the API key is not sent.
func (l *LipSync) Download(ctx context.Context, videoURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, providers.Permanent(err)
	}
	resp, err := l.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download video: %w", err)
	}
	defer resp.Body.Close()
	if err := statusError("download video", resp); err != nil {
		return nil, err
	}
	video, err := io.ReadAll(io.LimitReader(resp.Body, maxVideoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download video: %w", err)
	}
	switch {
	case len(video) == 0:
		return nil, errors.New("download video: it was empty")
	case len(video) > maxVideoBytes:
		return nil, providers.Permanent(errors.New("download video: larger than expected"))
	}
	return video, nil
}
