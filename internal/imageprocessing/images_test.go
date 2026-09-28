package imageprocessing

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// makePNGImage builds a w×h RGBA image filled with `c` and encodes
// it as PNG.
func makePNGImage(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func makeJPEGImage(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func decodeBoundsPNG(t *testing.T, b []byte) (w, h int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := img.Bounds()
	return r.Dx(), r.Dy()
}

func decodeBoundsJPEG(t *testing.T, b []byte) (w, h int) {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := img.Bounds()
	return r.Dx(), r.Dy()
}

func TestNormalizeToolResultImagesResizesBeforeHistory(t *testing.T) {
	input := makePNGImage(t, 2200, 1100, color.RGBA{10, 20, 30, 255})
	result := agent.AgentToolResult{
		Content: "screenshot",
		Images: []ai.ImageContent{{
			MimeType: "image/png",
			Data:     base64.StdEncoding.EncodeToString(input),
		}},
	}
	normalized := NormalizeToolResultImages(result, true)
	if len(normalized.Images) != 1 {
		t.Fatalf("normalized images = %d", len(normalized.Images))
	}
	decoded, err := base64.StdEncoding.DecodeString(normalized.Images[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	image, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatal(err)
	}
	if width := image.Bounds().Dx(); width > MaxLongestSide {
		t.Fatalf("normalized width = %d, want <= %d", width, MaxLongestSide)
	}
	if !strings.Contains(normalized.Content, "original 2200x1100, displayed at 2000x1000") {
		t.Fatalf("normalization hint missing: %q", normalized.Content)
	}
	if strings.Contains(normalized.Content, "converted from") {
		t.Fatalf("supported PNG resize must not report a format conversion: %q", normalized.Content)
	}

	unchanged := NormalizeToolResultImages(result, false)
	if unchanged.Images[0].Data != result.Images[0].Data || unchanged.Content != result.Content {
		t.Fatal("disabled normalization changed the tool result")
	}
}

func TestResizeOver2048Resizes(t *testing.T) {
	// 4096×2048 → longest side must be 2048 in output.
	// Use JPEG input to avoid OOM (4096×4096 PNG is huge).
	in := makeJPEGImage(t, 4096, 2048, color.RGBA{0, 128, 0, 255})
	out, mime, err := ResizeImageForLLM(in)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	var w, h int
	switch mime {
	case "image/png":
		w, h = decodeBoundsPNG(t, out)
	case "image/jpeg":
		w, h = decodeBoundsJPEG(t, out)
	default:
		t.Fatalf("unexpected mime: %q", mime)
	}
	longest := max(h, w)
	if longest != MaxLongestSide {
		t.Errorf("longest side %d want %d (mime=%s w=%d h=%d)", longest, MaxLongestSide, mime, w, h)
	}
	// Aspect ratio preserved within 1px.
	wantH := MaxLongestSide / 2
	if h < wantH-2 || h > wantH+2 {
		t.Errorf("aspect not preserved: got %dx%d want ~%dx%d", w, h, MaxLongestSide, wantH)
	}
}

// TestExifOrientationApplied pins the no-EXIF path: a plain JPEG below the
// resize threshold keeps its dimensions 1:1 and is returned untransformed.
func TestExifOrientationApplied(t *testing.T) {
	in := makeJPEGImage(t, 100, 200, color.RGBA{255, 0, 255, 255})
	out, mime, err := ResizeImageForLLM(in)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	var w, h int
	switch mime {
	case "image/png":
		w, h = decodeBoundsPNG(t, out)
	case "image/jpeg":
		w, h = decodeBoundsJPEG(t, out)
	}
	// Below threshold + no resize → dimensions preserved 1:1.
	if w != 100 || h != 200 {
		t.Errorf("dimensions changed unexpectedly: got %dx%d want 100x200 (mime=%s)", w, h, mime)
	}
}

func TestDetectSupportedImageMimeType(t *testing.T) {
	validPNG := append(append([]byte{}, pngSignature...), []byte{
		0, 0, 0, 13, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0,
		0, 0, 0, 0,
	}...)
	animatedPNG := append(append([]byte{}, validPNG...), []byte{
		0, 0, 0, 0, 'a', 'c', 'T', 'L',
		0, 0, 0, 0,
	}...)
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "jpeg", data: []byte{0xff, 0xd8, 0xff, 0xe0}, want: "image/jpeg"},
		{name: "jpeg jpegls unsupported", data: []byte{0xff, 0xd8, 0xff, 0xf7}, want: ""},
		{name: "png", data: validPNG, want: "image/png"},
		{name: "animated png unsupported", data: animatedPNG, want: ""},
		{name: "gif", data: []byte("GIF89a..."), want: "image/gif"},
		{name: "gif-prefixed text", data: []byte("GIF is not an image\n"), want: ""},
		{name: "truncated gif signature", data: []byte("GIF89"), want: ""},
		{name: "webp", data: []byte("RIFF\x00\x00\x00\x00WEBP"), want: "image/webp"},
		{name: "bmp", data: []byte{'B', 'M', 58, 0, 0, 0, 0, 0, 0, 0, 54, 0, 0, 0, 40, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 24, 0}, want: "image/bmp"},
		{name: "BM-prefixed text", data: []byte("BMAD method notes: keep this file as text\n"), want: ""},
		{name: "unknown", data: []byte{1, 2, 3, 4}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectSupportedImageMimeType(tt.data); got != tt.want {
				t.Fatalf("DetectSupportedImageMimeType() = %q, want %q", got, tt.want)
			}
		})
	}
}
