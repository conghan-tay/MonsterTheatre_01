package fake

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// testVideo is a one-second, 64 by 64 H.264 MP4 with no sound, small enough
// to keep in the repository and real enough for a browser to play. Made with:
//
//	ffmpeg -f lavfi -i color=c=0x7a5c99:s=64x64:d=1:r=10 -c:v libx264 \
//	  -profile:v baseline -level 3.0 -pix_fmt yuv420p -movflags +faststart -an testvideo.mp4
//
//go:embed testvideo.mp4
var testVideo []byte

// TestVideo returns the stand-in lip-sync video.
func TestVideo() []byte { return append([]byte(nil), testVideo...) }

// fakeVideoURL is where every stand-in video is, as Result reports it.
const fakeVideoURL = "fake://video.mp4"

// LipSync is a scripted providers.LipSync. Every video is the test clip. A
// script can fail a turn's submission (OpLipSync) or make its video take a
// while (LipSyncDelays). It keeps no state: a video's ID says when and for
// which room and turn it was submitted, so any worker can answer for it,
// after a restart too.
type LipSync struct {
	Scripts Scripts
	// Now is the clock, time.Now unless a test sets it.
	Now func() time.Time
}

var _ providers.LipSync = (*LipSync)(nil)

func (l *LipSync) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Upload keeps nothing and returns a URL naming the file.
func (l *LipSync) Upload(_ context.Context, req providers.UploadRequest) (string, error) {
	return "fake://uploads/" + req.Filename, nil
}

// Submit fails if the script says so, and otherwise returns an ID that
// carries the room, the turn, and the time.
func (l *LipSync) Submit(_ context.Context, req providers.LipSyncRequest) (string, error) {
	script, err := l.Scripts.For(req.RoomID)
	if err != nil {
		return "", err
	}
	if script.fails(OpLipSync, req.Turn, 1) {
		return "", ErrInjected
	}
	return fmt.Sprintf("fake/%s/%d/%d", req.RoomID, req.Turn, l.now().UnixMilli()), nil
}

// Result says the video is done once its scripted delay has passed.
func (l *LipSync) Result(_ context.Context, id string) (providers.LipSyncResult, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 4 || parts[0] != "fake" {
		return providers.LipSyncResult{}, providers.Permanent(fmt.Errorf("fake lip sync: unknown video %q", id))
	}
	turn, errTurn := strconv.Atoi(parts[2])
	submitted, errTime := strconv.ParseInt(parts[3], 10, 64)
	if errTurn != nil || errTime != nil {
		return providers.LipSyncResult{}, providers.Permanent(fmt.Errorf("fake lip sync: unknown video %q", id))
	}
	script, err := l.Scripts.For(parts[1])
	if err != nil {
		return providers.LipSyncResult{}, err
	}
	ready := time.UnixMilli(submitted).Add(time.Duration(script.LipSyncDelays[turn]) * time.Millisecond)
	if l.now().Before(ready) {
		return providers.LipSyncResult{Status: providers.LipSyncPending}, nil
	}
	return providers.LipSyncResult{Status: providers.LipSyncCompleted, VideoURL: fakeVideoURL}, nil
}

// Download returns the test clip.
func (l *LipSync) Download(_ context.Context, url string) ([]byte, error) {
	if url != fakeVideoURL {
		return nil, providers.Permanent(errors.New("fake lip sync: not a stand-in video"))
	}
	return TestVideo(), nil
}
