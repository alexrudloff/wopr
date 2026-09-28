// PNG conversion for terminal image display.
//
// Image decoding and EXIF orientation live in internal/imageprocessing.

package codingagent

import (
	"encoding/base64"

	"github.com/alexrudloff/wopr/internal/imageprocessing"
	"github.com/alexrudloff/wopr/tui"
)

// ConvertToPng prepares an image for the Kitty graphics protocol, which
// requires PNG (f=100), so a non-PNG base64 image is converted. PNG input is
// returned unchanged; nil means the conversion failed.
func ConvertToPng(base64Data, mimeType string) *tui.ConvertedImage {
	if mimeType == "image/png" {
		return &tui.ConvertedImage{Data: base64Data, MimeType: mimeType}
	}
	pngBytes := imageprocessing.ConvertImageBytesToPng(imageprocessing.DecodeBase64(base64Data))
	if pngBytes == nil {
		return nil
	}
	return &tui.ConvertedImage{
		Data:     base64.StdEncoding.EncodeToString(pngBytes),
		MimeType: "image/png",
	}
}

// maybeConvertImagesForKitty converts each pending image of comp to PNG. Each
// conversion runs on its own goroutine and hands its result to the main loop
// through runOnMain, which drops it once the run context ends. The component
// ignores a result whose source image was replaced meanwhile.
func (m *InteractiveMode) maybeConvertImagesForKitty(comp *tui.ToolExecutionComponent) {
	for _, req := range comp.PendingKittyImageConversions() {
		go func() {
			converted := ConvertToPng(req.Data, req.MimeType)
			m.runOnMain(m.runCtx, func() {
				if comp.ApplyConvertedImage(req, converted) {
					m.tuiInst.RequestRender()
				}
			})
		}()
	}
}
