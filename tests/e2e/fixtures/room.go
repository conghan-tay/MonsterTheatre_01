package fixtures

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
)

// SampleRoom returns a valid room submission with two small PNG portraits
// generated in code.
func SampleRoom(t testing.TB) client.RoomInput {
	t.Helper()
	return client.RoomInput{
		Fields: client.RoomFields{
			Scene: client.Scene{
				Topic:       "Two monsters argue about who ate the last biscuit",
				Description: "A cramped kitchen at midnight.",
				Genre:       "comedy",
			},
			CharacterA: client.Character{Name: "Grumble", Brief: "A tired swamp monster who denies everything."},
			CharacterB: client.Character{Name: "Fizz", Brief: "An excitable imp with crumbs on its face."},
		},
		PortraitA: PNG(t, "grumble.png", color.RGBA{R: 40, G: 120, B: 60, A: 255}),
		PortraitB: PNG(t, "fizz.png", color.RGBA{R: 200, G: 80, B: 160, A: 255}),
	}
}

// PNG returns a small solid-colour PNG file.
func PNG(t testing.TB, name string, fill color.Color) *client.File {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return &client.File{Name: name, ContentType: "image/png", Content: buf.Bytes()}
}
