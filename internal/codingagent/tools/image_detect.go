// Image format detection for the read tool.
package tools

import "github.com/alexrudloff/wopr/internal/imageprocessing"

// SupportedImageMime returns the supported still-image MIME type of a file's
// contents, or "" when it is not one. It looks only at the first
// ImageTypeSniffBytes.
//
// The read tool is one of only two producers of image bytes that later reach
// the image decoders, so this allowlist is what keeps formats outside it out
// of them.
func SupportedImageMime(data []byte) string {
	return imageprocessing.DetectSupportedImageMimeType(data[:min(len(data), imageprocessing.ImageTypeSniffBytes)])
}
