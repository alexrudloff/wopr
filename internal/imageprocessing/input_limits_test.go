package imageprocessing

import (
	"bytes"
	"image/color"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// The model profile replaces individual resize defaults, preserving
// the other dimensions and the source aspect ratio.
func TestResizeModelInputLimits(t *testing.T) {
	input := makePNGImage(t, 320, 160, color.RGBA{10, 20, 30, 255})
	for _, tc := range []struct {
		name          string
		options       *ai.ModelImageResizeOptions
		width, height int
	}{
		{"default", nil, 320, 160},
		{"width", &ai.ModelImageResizeOptions{MaxWidth: 100}, 100, 50},
		{"height", &ai.ModelImageResizeOptions{MaxHeight: 40}, 80, 40},
		{"both", &ai.ModelImageResizeOptions{MaxWidth: 100, MaxHeight: 40}, 80, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := prepareImageForLLM(input, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			if result.Width != tc.width || result.Height != tc.height {
				t.Fatalf("size=%dx%d, want %dx%d", result.Width, result.Height, tc.width, tc.height)
			}
			if tc.options == nil && !bytes.Equal(result.Data, input) {
				t.Fatal("within-limit input was re-encoded")
			}
		})
	}
}
