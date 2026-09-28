package imageprocessing

import (
	"encoding/base64"
	"strings"
)

// ConvertImageBytesToPng applies EXIF orientation before encoding PNG.
// Failure returns nil.
func ConvertImageBytesToPng(data []byte) []byte {
	img, err := decodeAutoOriented(data)
	if err != nil {
		return nil
	}
	png, err := encodePNG(img)
	if err != nil {
		return nil
	}
	return png
}

// DecodeBase64 decodes standard or URL-safe base64, padded or not, ignoring
// whitespace. Invalid input returns nil.
func DecodeBase64(data string) []byte {
	data = strings.Map(func(r rune) rune {
		switch r {
		case '-':
			return '+'
		case '_':
			return '/'
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, data)
	out, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="))
	if err != nil {
		return nil
	}
	return out
}
