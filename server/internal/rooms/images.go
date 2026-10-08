package rooms

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg" // register the JPEG decoder
	_ "image/png"  // register the PNG decoder

	_ "golang.org/x/image/webp" // register the WebP decoder
)

const (
	// MaxPortraitBytes is the largest portrait accepted.
	MaxPortraitBytes = 5 << 20
	// maxPortraitDimension is the largest width or height accepted.
	maxPortraitDimension = 4096
)

var (
	errNotAnImage       = errors.New("Must be a JPEG, PNG, or WebP image.")
	errImageTooBig      = errors.New("Must be at most 4096 by 4096 pixels.")
	errAnimatedNotAllow = errors.New("Animated images are not supported.")
)

// contentTypes maps the decoder's format name to the content type served.
var contentTypes = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
}

// DetectImage checks that data is a supported still image within the size
// limit and returns its content type. The type comes from the bytes
// themselves, never from what the uploader claimed.
func DetectImage(data []byte) (string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", errNotAnImage
	}
	contentType, ok := contentTypes[format]
	if !ok {
		return "", errNotAnImage
	}
	if config.Width > maxPortraitDimension || config.Height > maxPortraitDimension {
		return "", errImageTooBig
	}
	if format == "webp" && isAnimatedWebP(data) {
		return "", errAnimatedNotAllow
	}
	return contentType, nil
}

// isAnimatedWebP reports whether an extended-format WebP has its animation
// flag set. Layout: "RIFF" size "WEBP" "VP8X" chunk-size flags..., with the
// animation flag in bit 1 of the flags byte at offset 20.
func isAnimatedWebP(data []byte) bool {
	const animationFlag = 0x02
	return len(data) > 20 && string(data[12:16]) == "VP8X" && data[20]&animationFlag != 0
}
