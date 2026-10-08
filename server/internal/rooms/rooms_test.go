package rooms

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validInput() CreateInput {
	return CreateInput{
		Scene:      Scene{Topic: "A haunted bakery", Description: "Flour everywhere.", Genre: "comedy"},
		CharacterA: CharacterInput{Name: "Grumble", Brief: "Tired."},
		CharacterB: CharacterInput{Name: "Fizz", Brief: "Excitable."},
	}
}

func TestValidInputHasNoProblems(t *testing.T) {
	in := validInput()
	in.Normalize()
	require.Empty(t, in.Validate())
}

func TestOptionalFieldsMayBeEmpty(t *testing.T) {
	in := validInput()
	in.Scene.Description, in.Scene.Genre, in.CharacterA.Brief, in.CharacterB.Brief = "", "", "", ""
	in.Normalize()
	require.Empty(t, in.Validate())
}

func TestNormalizeTrimsBeforeValidation(t *testing.T) {
	in := validInput()
	in.CharacterA.Name = "  Grumble \n"
	in.Scene.Topic = "   "
	in.Normalize()

	require.Equal(t, "Grumble", in.CharacterA.Name)
	require.Equal(t, map[string]string{"scene.topic": "Required."}, in.Validate(), "whitespace alone does not satisfy a required field")
}

func TestEachFieldIsHeldToItsLimit(t *testing.T) {
	limits := map[string]struct {
		set func(*CreateInput, string)
		max int
	}{
		"scene.topic":       {func(in *CreateInput, v string) { in.Scene.Topic = v }, 200},
		"scene.description": {func(in *CreateInput, v string) { in.Scene.Description = v }, 1000},
		"scene.genre":       {func(in *CreateInput, v string) { in.Scene.Genre = v }, 80},
		"characterA.name":   {func(in *CreateInput, v string) { in.CharacterA.Name = v }, 80},
		"characterA.brief":  {func(in *CreateInput, v string) { in.CharacterA.Brief = v }, 400},
		"characterB.name":   {func(in *CreateInput, v string) { in.CharacterB.Name = v }, 80},
		"characterB.brief":  {func(in *CreateInput, v string) { in.CharacterB.Brief = v }, 400},
	}
	for field, limit := range limits {
		atLimit := validInput()
		limit.set(&atLimit, strings.Repeat("x", limit.max))
		require.Empty(t, atLimit.Validate(), "%s at exactly %d characters is accepted", field, limit.max)

		over := validInput()
		limit.set(&over, strings.Repeat("x", limit.max+1))
		problems := over.Validate()
		require.Len(t, problems, 1, field)
		require.Contains(t, problems[field], "at most", field)
	}
}

func TestLimitsCountCharactersNotBytes(t *testing.T) {
	in := validInput()
	in.CharacterA.Name = strings.Repeat("怪", 80) // 80 characters, 240 bytes
	require.Empty(t, in.Validate())
}

// --- images ---

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func encodeJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.White)
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// webpHeader builds the 30-byte header of an extended-format WebP, which is
// all DetectImage reads. Go cannot encode WebP, so the header is built by
// hand; it is enough to exercise detection but is not a viewable image.
func webpHeader(flags byte, width, height int) []byte {
	b := []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00")
	b = append(b, flags, 0, 0, 0)
	w, h := width-1, height-1
	b = append(b, byte(w), byte(w>>8), byte(w>>16), byte(h), byte(h>>8), byte(h>>16))
	return b
}

func TestDetectImageAcceptsSupportedFormatsByTheirBytes(t *testing.T) {
	for want, data := range map[string][]byte{
		"image/png":  encodePNG(t, 16, 16),
		"image/jpeg": encodeJPEG(t),
		"image/webp": webpHeader(0, 64, 64),
	} {
		got, err := DetectImage(data)
		require.NoError(t, err, want)
		require.Equal(t, want, got)
	}
}

func TestDetectImageRejectsWhatIsNotASupportedStillImage(t *testing.T) {
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	for name, data := range map[string][]byte{
		"plain text":    []byte("this is plain text, whatever the name says"),
		"empty":         {},
		"gif":           gif,
		"truncated png": encodePNG(t, 16, 16)[:12],
		"animated webp": webpHeader(0x02, 64, 64),
	} {
		_, err := DetectImage(data)
		require.Error(t, err, name)
	}
}

func TestDetectImageEnforcesTheDimensionLimit(t *testing.T) {
	_, err := DetectImage(encodePNG(t, 4096, 1))
	require.NoError(t, err, "4096 wide is allowed")

	_, err = DetectImage(encodePNG(t, 4097, 1))
	require.ErrorIs(t, err, errImageTooBig)
	_, err = DetectImage(webpHeader(0, 64, 5000))
	require.ErrorIs(t, err, errImageTooBig)
}

// --- share links ---

const testSecret = "0123456789abcdef0123456789abcdef"

func TestShareLinkIsStableAndVerifiable(t *testing.T) {
	links, err := NewShareLinks(testSecret, "https://theatre.example")
	require.NoError(t, err)
	room := "3b9d7e10-1111-4222-8333-444455556666"

	url := links.URL(room)
	require.Equal(t, url, links.URL(room), "the same room always gets the same link")
	require.True(t, strings.HasPrefix(url, "https://theatre.example/join/"+room+"/"))

	token := links.Token(room)
	require.Len(t, token, 43, "a SHA-256 MAC, base64url without padding")
	require.True(t, links.Verify(room, token))
}

func TestShareLinkTokenIsBoundToItsRoomAndSecret(t *testing.T) {
	links, _ := NewShareLinks(testSecret, "https://theatre.example")
	roomA, roomB := "room-a", "room-b"

	require.False(t, links.Verify(roomB, links.Token(roomA)), "a token for one room does not open another")
	require.False(t, links.Verify(roomA, ""), "an empty token is refused")
	require.False(t, links.Verify(roomA, links.Token(roomA)+"x"))

	rotated, _ := NewShareLinks("ffffffffffffffffffffffffffffffff", "https://theatre.example")
	require.False(t, rotated.Verify(roomA, links.Token(roomA)), "changing the secret invalidates old links")
}

func TestShareLinksRefuseAShortSecret(t *testing.T) {
	for _, secret := range []string{"", "short", strings.Repeat("x", 31)} {
		_, err := NewShareLinks(secret, "https://theatre.example")
		require.ErrorContains(t, err, "SHARE_LINK_SECRET", "secret of length %d", len(secret))
	}
	_, err := NewShareLinks(strings.Repeat("x", 32), "https://theatre.example")
	require.NoError(t, err)
}
