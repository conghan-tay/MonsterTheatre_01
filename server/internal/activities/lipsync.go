package activities

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// The lip-sync activities turn a voiced line into a video of its speaker's
// portrait speaking it (docs/decisions.md, D055). They are three, because
// the three steps are retried differently: uploading is safe to repeat,
// submitting is not (a submission lost on its way back may still have
// started a video, and been billed), and waiting is long.

// maxInputBytes bounds a portrait or a recording read for upload. Portraits
// are at most 5 MiB; a recording is a few hundred kilobytes.
const maxInputBytes = 20 << 20

// VideoKey is where a turn's lip-sync video is stored.
func VideoKey(performanceID string, turn int) string {
	return fmt.Sprintf("performances/%s/turn-%d.mp4", performanceID, turn)
}

// LipSyncInputs names a turn's portrait and recording.
type LipSyncInputs struct {
	PerformanceID string
	RoomID        string
	Turn          int
	PortraitKey   string
}

// LipSyncUploads is where the lip-sync service can read a turn's inputs.
type LipSyncUploads struct {
	ImageURL string
	AudioURL string
}

// imageExtension names an uploaded portrait's file by its type, which is
// one of the three a room accepts.
var imageExtension = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}

// UploadLipSyncInputs sends the speaker's portrait and the turn's recording
// to the lip-sync service, which reads its inputs from URLs. Repeating it
// only uploads the two files again.
func (a *Activities) UploadLipSyncInputs(ctx context.Context, in LipSyncInputs) (LipSyncUploads, error) {
	portrait, portraitType, err := a.read(ctx, in.PortraitKey)
	if err != nil {
		return LipSyncUploads{}, err
	}
	recording, _, err := a.read(ctx, AudioKey(in.PerformanceID, in.Turn))
	if err != nil {
		return LipSyncUploads{}, err
	}
	name := fmt.Sprintf("%s-turn-%d", in.PerformanceID, in.Turn)
	imageURL, err := a.LipSync.Upload(ctx, providers.UploadRequest{
		RoomID: in.RoomID, Turn: in.Turn, Filename: name + "-portrait" + imageExtension[portraitType],
		ContentType: portraitType, Data: portrait,
	})
	if err != nil {
		return LipSyncUploads{}, activityError(err)
	}
	audioURL, err := a.LipSync.Upload(ctx, providers.UploadRequest{
		RoomID: in.RoomID, Turn: in.Turn, Filename: name + ".mp3", ContentType: "audio/mpeg", Data: recording,
	})
	if err != nil {
		return LipSyncUploads{}, activityError(err)
	}
	return LipSyncUploads{ImageURL: imageURL, AudioURL: audioURL}, nil
}

// read loads a stored object whole, with its content type.
func (a *Activities) read(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := a.Objects.Get(ctx, key)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", key, err)
	}
	defer obj.Body.Close()
	data, err := io.ReadAll(io.LimitReader(obj.Body, maxInputBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", key, err)
	}
	return data, obj.ContentType, nil
}

// LipSyncSubmission asks for one video.
type LipSyncSubmission struct {
	RoomID     string
	Turn       int
	ImageURL   string
	AudioURL   string
	Resolution string
}

// SubmitLipSync starts a turn's video and returns the service's ID for it.
// The workflow tries it once only.
func (a *Activities) SubmitLipSync(ctx context.Context, in LipSyncSubmission) (string, error) {
	id, err := a.LipSync.Submit(ctx, providers.LipSyncRequest{
		RoomID: in.RoomID, Turn: in.Turn, ImageURL: in.ImageURL, AudioURL: in.AudioURL, Resolution: in.Resolution,
	})
	if err != nil {
		return "", activityError(err)
	}
	return id, nil
}

// LipSyncWait is a submitted video to wait for.
type LipSyncWait struct {
	PerformanceID string
	RoomID        string
	Turn          int
	PredictionID  string
}

// AwaitLipSync asks whether a video is done, every LipSyncPollInterval,
// until it is; then it downloads the video, stores it, and returns its key.
// It says it is alive after every answer, so that if its worker dies the
// wait is taken up by another, which asks about the same video.
//
// An answer that may be passing (a rate limit, a server error) is waited
// through. A video that failed, or an answer that says the request itself
// is wrong, ends it for good. How long it may take in all is the workflow's
// to say.
func (a *Activities) AwaitLipSync(ctx context.Context, in LipSyncWait) (string, error) {
	for polls := 1; ; polls++ {
		result, err := a.LipSync.Result(ctx, in.PredictionID)
		switch {
		case err != nil && providers.IsPermanent(err):
			return "", activityError(err)
		case err != nil:
			activity.GetLogger(ctx).Warn("lip sync result unavailable; asking again", "turn", in.Turn, "error", err)
		case result.Status == providers.LipSyncFailed:
			return "", temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("turn %d: lip sync failed: %s", in.Turn, result.Error), "LipSyncFailed", nil)
		case result.Status == providers.LipSyncCompleted:
			return a.storeVideo(ctx, in, result.VideoURL)
		}
		activity.RecordHeartbeat(ctx, polls)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(a.LipSyncPollInterval):
		}
	}
}

// storeVideo downloads a finished video and keeps it with the recordings:
// the service keeps its outputs for only a few days.
func (a *Activities) storeVideo(ctx context.Context, in LipSyncWait, url string) (string, error) {
	video, err := a.LipSync.Download(ctx, url)
	if err != nil {
		return "", activityError(err)
	}
	key := VideoKey(in.PerformanceID, in.Turn)
	if err := a.Objects.Put(ctx, key, bytes.NewReader(video), "video/mp4"); err != nil {
		return "", err
	}
	return key, nil
}
