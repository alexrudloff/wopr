// Image resize + re-encode policy for CLI/LLM image attachments, plus the
// EXIF orientation reader. PNG conversion for Kitty lives in image_convert.go.
//
// Uses the Go standard library plus golang.org/x/image:
//  1. Decode + apply EXIF orientation once.
//  2. If needed, resize to fit within 2048x2048 and a budget of 2,500
//     32-pixel tiles (about 2.56 megapixels).
//  3. Try PNG first, then JPEG quality steps until the base64 payload fits
//     under 4.5 MB.
//  4. If still too large, shrink dimensions by 25% and retry.
package imageprocessing

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // decoder for the GIF entry of the supported-format allowlist
	"image/jpeg"  // decoder for JPEG plus the JPEG re-encoder
	"image/png"   // decoder for PNG plus the PNG re-encoder
	"math"
	"os"
	"slices"
	"strings"

	_ "golang.org/x/image/bmp" // decoder for the BMP entry of the supported-format allowlist
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder for the WebP entry of the supported-format allowlist

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

const (
	MaxLongestSide  = 2048
	MaxEncodedBytes = int(4.5 * 1024 * 1024) // base64 payload bytes
	JPEGQuality     = 85
	// tileSize and maxTiles bound an image's area: models bill and see images
	// in 32-pixel tiles, so a wide screenshot inside 2048x2048 can still be
	// far more tiles than the model uses.
	tileSize = 32
	maxTiles = 2500
)

var ErrImageTooLarge = errors.New("image: could not be resized below inline image size limit")

// errPNGConversionFailed reports that the declared format
// is unsupported and convertImageBytesToPng could not re-encode it.
var errPNGConversionFailed = errors.New("image: could not convert to png")

type preparedImageResult struct {
	Data           []byte
	MIME           string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
	WasResized     bool
}

// NormalizeToolResultImages processes data images as they enter history. With
// auto-resize enabled, oversized images are resized once; disabled, unsupported
// still-image formats are still converted to a supported inline format (no
// resize): normalization runs before the auto-resize branch. Decode/processing failures preserve the
// original block; conversion and resize hints are appended to the tool result.
func NormalizeToolResultImages(result agent.AgentToolResult, autoResize bool) agent.AgentToolResult {
	return NormalizeToolResultImagesWithOptions(result, autoResize, nil)
}

// NormalizeToolResultImagesWithOptions applies the active model profile after
// extension hooks. Failed image processing retains the original image block.
func NormalizeToolResultImagesWithOptions(result agent.AgentToolResult, autoResize bool, options *ai.ModelImageResizeOptions) agent.AgentToolResult {
	if len(result.Images) == 0 {
		return result
	}
	images := make([]ai.ImageContent, 0, len(result.Images))
	var hints []string
	changed := false
	for _, image := range result.Images {
		if image.Data == "" {
			images = append(images, image)
			continue
		}
		decoded := DecodeBase64(image.Data)
		processed, mime, hint, err := ProcessImage(decoded, image.MimeType, autoResize, options)
		if err != nil {
			images = append(images, image)
			continue
		}
		normalized := image
		normalized.Data = base64.StdEncoding.EncodeToString(processed)
		normalized.MimeType = mime
		images = append(images, normalized)
		if normalized.Data != image.Data || normalized.MimeType != image.MimeType {
			changed = true
		}
		if hint != "" {
			hints = append(hints, hint)
			changed = true
		}
	}
	if !changed {
		return result
	}
	result.Images = images
	if len(hints) > 0 {
		if result.Content != "" {
			result.Content += "\n"
		}
		result.Content += strings.Join(hints, "\n")
	}
	return result
}

// convertToolResultImageOnly normalizes without resizing: a supported declared
// format passes through with its canonical MIME while an unsupported one is
// re-encoded to PNG and reports the conversion: but no resize is applied, even
// when the image exceeds the inline size limit.
func convertToolResultImageOnly(decoded []byte, declaredMIME string) ([]byte, string, string, error) {
	if norm := normalizeSupportedImageMIME(declaredMIME); norm != "" {
		return decoded, norm, "", nil
	}
	png := ConvertImageBytesToPng(decoded)
	if png == nil {
		return nil, "", "", errPNGConversionFailed
	}
	return png, "image/png", imageConversionHint(declaredMIME, "image/png"), nil
}

// ResizeImageForLLM applies the CLI image policy and returns the
// processed bytes plus the resulting MIME type.
func ResizeImageForLLM(in []byte) ([]byte, string, error) {
	result, err := prepareImageForLLM(in, nil)
	if err != nil {
		return nil, "", err
	}
	return result.Data, result.MIME, nil
}

// PrepareCLIImageAttachment processes one CLI image file into the attached
// image payload plus the dimension note (empty when not resized).
func PrepareCLIImageAttachment(in []byte, inputMIME string) ([]byte, string, string, error) {
	return ProcessImage(in, inputMIME, true, nil)
}

// ProcessImage normalizes unsupported formats before applying a model's resize
// profile. A false autoResize flag still converts unsupported formats. Errors
// carry an omission hint; callers decide whether to keep a tool image.
func ProcessImage(in []byte, inputMIME string, autoResize bool, options *ai.ModelImageResizeOptions) ([]byte, string, string, error) {
	normalized, mime, conversionHint, err := convertToolResultImageOnly(in, inputMIME)
	if err != nil {
		return nil, "", "", errors.New("[Image omitted: could not be converted to a supported inline image format.]")
	}
	if !autoResize {
		return normalized, mime, conversionHint, nil
	}
	result, err := prepareImageForLLM(normalized, options)
	if err != nil {
		return nil, "", "", errors.New("[Image omitted: could not be resized below the inline image size limit.]")
	}
	if !result.WasResized {
		result.MIME = mime
	}
	return result.Data, result.MIME, joinImageHints(imageConversionHint(inputMIME, result.MIME), formatDimensionNote(result)), nil
}

// imageConversionHint reports a format conversion only when the declared input
// MIME was an unsupported still-image format that had to be re-encoded to a
// supported one. A supported input that is merely re-encoded while resizing
// does not report a conversion.
func imageConversionHint(inputMIME, outputMIME string) string {
	from := baseImageMIME(inputMIME)
	to := baseImageMIME(outputMIME)
	if from == "" || from == to || isPassthroughImageMIME(from) {
		return ""
	}
	return fmt.Sprintf("[Image converted from %s to %s.]", from, to)
}

// baseImageMIME strips any parameters and normalizes case.
func baseImageMIME(mime string) string {
	base, _, _ := strings.Cut(mime, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// normalizeSupportedImageMIME returns the canonical MIME for a supported
// still-image format that passes through without a conversion, or "" for an unsupported
// declared type.
func normalizeSupportedImageMIME(mime string) string {
	switch baseImageMIME(mime) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	}
	return ""
}

// isPassthroughImageMIME reports whether ProcessImage passes the declared
// format through without a conversion.
func isPassthroughImageMIME(mime string) bool {
	return normalizeSupportedImageMIME(mime) != ""
}

// joinImageHints concatenates non-empty hints with newlines (conversion hint
// before dimension note).
func joinImageHints(hints ...string) string {
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		if h != "" {
			out = append(out, h)
		}
	}
	return strings.Join(out, "\n")
}

// DetectSupportedImageMimeType returns a supported
// still-image MIME type for the provided bytes, or "" when unsupported.
//
// The formats named here are also the only decoders this package links, so a
// format outside the allowlist has no decoder to reach. Adding a MIME type here
// or to tools.SupportedImageMime without adding its decoder import decodes
// nothing; adding the decoder import widens the attack surface of the image
// pipeline to that format's parser.
func DetectSupportedImageMimeType(buffer []byte) string {
	if bytes.HasPrefix(buffer, []byte{0xff, 0xd8, 0xff}) {
		if len(buffer) > 3 && buffer[3] == 0xf7 {
			return ""
		}
		return "image/jpeg"
	}
	if bytes.HasPrefix(buffer, pngSignature) {
		if isPNG(buffer) && !isAnimatedPNG(buffer) {
			return "image/png"
		}
		return ""
	}
	if startsWithASCII(buffer, 0, "GIF87a") || startsWithASCII(buffer, 0, "GIF89a") {
		return "image/gif"
	}
	if startsWithASCII(buffer, 0, "RIFF") && startsWithASCII(buffer, 8, "WEBP") {
		return "image/webp"
	}
	if startsWithASCII(buffer, 0, "BM") && isBMP(buffer) {
		return "image/bmp"
	}
	return ""
}

// isBMP reports a plausible BMP header: the header sizes, color planes and
// bits per pixel must be consistent, so a text file starting with "BM" is
// not taken for an image.
func isBMP(buffer []byte) bool {
	if len(buffer) < 26 {
		return false
	}
	declaredFileSize := int(binary.LittleEndian.Uint32(buffer[2:]))
	pixelDataOffset := int(binary.LittleEndian.Uint32(buffer[10:]))
	dibHeaderSize := int(binary.LittleEndian.Uint32(buffer[14:]))
	if declaredFileSize != 0 && declaredFileSize < 26 {
		return false
	}
	if pixelDataOffset < 14+dibHeaderSize {
		return false
	}
	if declaredFileSize != 0 && pixelDataOffset >= declaredFileSize {
		return false
	}
	var colorPlanes, bitsPerPixel int
	switch {
	case dibHeaderSize == 12:
		colorPlanes, bitsPerPixel = int(binary.LittleEndian.Uint16(buffer[22:])), int(binary.LittleEndian.Uint16(buffer[24:]))
	case dibHeaderSize >= 40 && dibHeaderSize <= 124:
		if len(buffer) < 30 {
			return false
		}
		colorPlanes, bitsPerPixel = int(binary.LittleEndian.Uint16(buffer[26:])), int(binary.LittleEndian.Uint16(buffer[28:]))
	default:
		return false
	}
	return colorPlanes == 1 && slices.Contains([]int{1, 4, 8, 16, 24, 32}, bitsPerPixel)
}

// ImageTypeSniffBytes is the sniff window: detection looks only at a file's
// first 4100 bytes.
const ImageTypeSniffBytes = 4100

// DetectSupportedImageMimeTypeFromFile reads the sniff window from a
// file and returns the supported still-image MIME type, or "" when unsupported.
func DetectSupportedImageMimeTypeFromFile(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, ImageTypeSniffBytes)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return ""
	}
	return DetectSupportedImageMimeType(buf[:n])
}

func prepareImageForLLM(in []byte, options *ai.ModelImageResizeOptions) (preparedImageResult, error) {
	limits := ai.ModelImageResizeOptions{MaxWidth: MaxLongestSide, MaxHeight: MaxLongestSide, MaxBytes: MaxEncodedBytes, JPEGQuality: JPEGQuality}
	if options != nil {
		if options.MaxWidth != 0 {
			limits.MaxWidth = options.MaxWidth
		}
		if options.MaxHeight != 0 {
			limits.MaxHeight = options.MaxHeight
		}
		if options.MaxBytes != 0 {
			limits.MaxBytes = options.MaxBytes
		}
		if options.JPEGQuality != 0 {
			limits.JPEGQuality = options.JPEGQuality
		}
	}
	if len(in) == 0 {
		return preparedImageResult{}, errors.New("image: empty input")
	}

	src, err := decodeAutoOriented(in)
	if err != nil {
		return preparedImageResult{}, fmt.Errorf("image: decode: %w", err)
	}
	bounds := src.Bounds()
	originalWidth, originalHeight := bounds.Dx(), bounds.Dy()
	srcFormat := detectFormat(in)
	srcMime := mimeForFormat(srcFormat)
	inputBase64Size := encodedSizeBase64(in)

	if originalWidth <= limits.MaxWidth && originalHeight <= limits.MaxHeight && tiles(originalWidth, originalHeight) <= maxTiles && inputBase64Size < limits.MaxBytes && srcFormat != "bmp" {
		return preparedImageResult{
			Data:           in,
			MIME:           srcMime,
			OriginalWidth:  originalWidth,
			OriginalHeight: originalHeight,
			Width:          originalWidth,
			Height:         originalHeight,
			WasResized:     false,
		}, nil
	}

	currentWidth, currentHeight := fitWithinBounds(originalWidth, originalHeight, limits.MaxWidth, limits.MaxHeight)
	currentWidth, currentHeight = fitTileBudget(currentWidth, currentHeight)
	qualitySteps := []int{limits.JPEGQuality}
	for _, quality := range []int{85, 70, 55, 40} {
		if quality != limits.JPEGQuality {
			qualitySteps = append(qualitySteps, quality)
		}
	}

	for {
		resized := resizeImage(src, currentWidth, currentHeight)
		candidates, err := encodeCandidates(resized, qualitySteps)
		if err != nil {
			return preparedImageResult{}, err
		}
		for _, candidate := range candidates {
			if candidate.EncodedSize < limits.MaxBytes {
				return preparedImageResult{
					Data:           candidate.Data,
					MIME:           candidate.MIME,
					OriginalWidth:  originalWidth,
					OriginalHeight: originalHeight,
					Width:          currentWidth,
					Height:         currentHeight,
					WasResized:     true,
				}, nil
			}
		}
		if currentWidth == 1 && currentHeight == 1 {
			break
		}
		nextWidth := currentWidth
		if nextWidth > 1 {
			nextWidth = max(1, int(float64(currentWidth)*0.75))
		}
		nextHeight := currentHeight
		if nextHeight > 1 {
			nextHeight = max(1, int(float64(currentHeight)*0.75))
		}
		if nextWidth == currentWidth && nextHeight == currentHeight {
			break
		}
		currentWidth, currentHeight = nextWidth, nextHeight
	}

	return preparedImageResult{}, ErrImageTooLarge
}

// exifOrientation is the EXIF orientation tag value, 1..8. getExifOrientation
// reports 1 (normal) whenever the tag is absent, unreadable, or out of range.
type exifOrientation int

const (
	orientationNormal     exifOrientation = 1
	orientationFlipH      exifOrientation = 2
	orientationRotate180  exifOrientation = 3
	orientationFlipV      exifOrientation = 4
	orientationTranspose  exifOrientation = 5
	orientationRotate270  exifOrientation = 6
	orientationTransverse exifOrientation = 7
	orientationRotate90   exifOrientation = 8
)

// decodeAutoOriented decodes an image and applies its EXIF orientation tag.
// Only the formats
// whose decoders this package links are decodable; see
// DetectSupportedImageMimeType.
func decodeAutoOriented(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return fixOrientation(img, getExifOrientation(data)), nil
}

// getExifOrientation locates the TIFF block of a JPEG APP1 "Exif" segment or a WebP EXIF chunk
// and reads IFD0's orientation tag. Every failure yields orientationNormal.
func getExifOrientation(data []byte) exifOrientation {
	tiffOffset := -1
	switch {
	case len(data) >= 2 && data[0] == 0xff && data[1] == 0xd8:
		tiffOffset = findJpegTiffOffset(data)
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		tiffOffset = findWebpTiffOffset(data)
	}
	if tiffOffset == -1 {
		return orientationNormal
	}
	return readOrientationFromTiff(data, tiffOffset)
}

// findJpegTiffOffset walks JPEG marker segments, skipping
// 0xff fill bytes and any APP1 segment that is not "Exif\0\0" (for example
// XMP), and returns the TIFF header offset or -1.
func findJpegTiffOffset(data []byte) int {
	offset := 2
	for offset < len(data)-1 {
		if data[offset] != 0xff {
			return -1
		}
		marker := data[offset+1]
		if marker == 0xff {
			offset++
			continue
		}
		if marker == 0xe1 {
			if offset+4 >= len(data) {
				return -1
			}
			segmentStart := offset + 4
			if segmentStart+6 > len(data) {
				return -1
			}
			if hasExifHeader(data, segmentStart) {
				return segmentStart + 6
			}
		}
		if offset+4 > len(data) {
			return -1
		}
		length := int(data[offset+2])<<8 | int(data[offset+3])
		offset += 2 + length
	}
	return -1
}

// findWebpTiffOffset walks RIFF chunks after the WEBP
// header and returns the TIFF offset inside the EXIF chunk, skipping an
// optional "Exif\0\0" prefix, or -1.
func findWebpTiffOffset(data []byte) int {
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		// The chunk size is read as a signed 32-bit value.
		chunkSize := int(int32(binary.LittleEndian.Uint32(data[offset+4 : offset+8])))
		dataStart := offset + 8
		if chunkID == "EXIF" {
			if dataStart+chunkSize > len(data) {
				return -1
			}
			if chunkSize >= 6 && hasExifHeader(data, dataStart) {
				return dataStart + 6
			}
			return dataStart
		}
		// RIFF chunks are padded to even size.
		next := dataStart + chunkSize + chunkSize%2
		if next <= offset {
			// A negative size would revisit this chunk forever; report not found.
			return -1
		}
		offset = next
	}
	return -1
}

func hasExifHeader(data []byte, offset int) bool {
	return offset >= 0 && offset+6 <= len(data) && string(data[offset:offset+6]) == "Exif\x00\x00"
}

// readOrientationFromTiff reads IFD0's orientation: byte order "II" is little-endian,
// anything else big-endian; IFD0 entries are 12 bytes and the SHORT value sits
// at entry offset 8. Out-of-range values read as normal.
func readOrientationFromTiff(data []byte, tiffStart int) exifOrientation {
	if tiffStart+8 > len(data) {
		return orientationNormal
	}
	var order binary.ByteOrder = binary.BigEndian
	if data[tiffStart] == 0x49 && data[tiffStart+1] == 0x49 {
		order = binary.LittleEndian
	}
	ifdOffset := int64(order.Uint32(data[tiffStart+4 : tiffStart+8]))
	if order == binary.LittleEndian {
		// The little-endian IFD offset is read as a signed 32-bit value.
		ifdOffset = int64(int32(ifdOffset))
	}
	ifdStart := int64(tiffStart) + ifdOffset
	if ifdStart+2 > int64(len(data)) {
		return orientationNormal
	}
	if ifdStart < 0 {
		// A negative offset reads as a zero entry count.
		return orientationNormal
	}
	entryCount := int64(order.Uint16(data[ifdStart : ifdStart+2]))
	for i := range entryCount {
		entryPos := ifdStart + 2 + i*12
		if entryPos+12 > int64(len(data)) {
			return orientationNormal
		}
		if order.Uint16(data[entryPos:entryPos+2]) == 0x0112 {
			value := order.Uint16(data[entryPos+8 : entryPos+10])
			if value >= 1 && value <= 8 {
				return exifOrientation(value)
			}
			return orientationNormal
		}
	}
	return orientationNormal
}

// fixOrientation applies the transform that makes an EXIF-tagged image upright.
// Rotations are counter-clockwise, matching the EXIF tag definitions.
func fixOrientation(img image.Image, o exifOrientation) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	switch o {
	case orientationFlipH:
		return remapPixels(img, w, h, func(x, y int) (int, int) { return w - 1 - x, y })
	case orientationFlipV:
		return remapPixels(img, w, h, func(x, y int) (int, int) { return x, h - 1 - y })
	case orientationRotate180:
		return remapPixels(img, w, h, func(x, y int) (int, int) { return w - 1 - x, h - 1 - y })
	case orientationTranspose:
		return remapPixels(img, h, w, func(x, y int) (int, int) { return y, x })
	case orientationTransverse:
		return remapPixels(img, h, w, func(x, y int) (int, int) { return w - 1 - y, h - 1 - x })
	case orientationRotate90:
		return remapPixels(img, h, w, func(x, y int) (int, int) { return w - 1 - y, x })
	case orientationRotate270:
		return remapPixels(img, h, w, func(x, y int) (int, int) { return y, h - 1 - x })
	case orientationNormal:
		return img
	}
	return img
}

// remapPixels builds a dstW×dstH NRGBA image whose pixel (x,y) is the source
// pixel named by src, expressed in source coordinates relative to the source
// bounds origin. NRGBA output keeps alpha non-premultiplied so imageHasAlpha
// reads the same channel the encoders write.
func remapPixels(img image.Image, dstW, dstH int, src func(x, y int) (int, int)) *image.NRGBA {
	srcNRGBA := toNRGBA(img)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for y := range dstH {
		for x := range dstW {
			sx, sy := src(x, y)
			s := srcNRGBA.PixOffset(sx, sy)
			d := dst.PixOffset(x, y)
			copy(dst.Pix[d:d+4], srcNRGBA.Pix[s:s+4])
		}
	}
	return dst
}

// toNRGBA returns img as an origin-anchored NRGBA image, reusing it when it is
// already one.
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Bounds().Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// resizeImage scales img to width×height with the CatmullRom kernel, the
// highest-quality resampler in golang.org/x/image/draw.
func resizeImage(img image.Image, width, height int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	return dst
}

type encodedCandidate struct {
	Data        []byte
	EncodedSize int
	MIME        string
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func encodeCandidates(img image.Image, jpegQualities []int) ([]encodedCandidate, error) {
	pngBytes, err := encodePNG(img)
	if err != nil {
		return nil, fmt.Errorf("image: encode png: %w", err)
	}
	candidates := []encodedCandidate{{
		Data:        pngBytes,
		EncodedSize: encodedSizeBase64(pngBytes),
		MIME:        "image/png",
	}}
	for _, quality := range jpegQualities {
		jpegBytes, err := encodeJPEG(img, quality)
		if err != nil {
			continue
		}
		candidates = append(candidates, encodedCandidate{
			Data:        jpegBytes,
			EncodedSize: encodedSizeBase64(jpegBytes),
			MIME:        "image/jpeg",
		})
	}
	return candidates, nil
}

func fitWithinBounds(width, height, maxWidth, maxHeight int) (int, int) {
	targetWidth, targetHeight := width, height
	if targetWidth > maxWidth {
		targetHeight = int(math.Round(float64(targetHeight) * float64(maxWidth) / float64(targetWidth)))
		targetWidth = maxWidth
	}
	if targetHeight > maxHeight {
		targetWidth = int(math.Round(float64(targetWidth) * float64(maxHeight) / float64(targetHeight)))
		targetHeight = maxHeight
	}
	return max(1, targetWidth), max(1, targetHeight)
}

// tiles is the number of tileSize tiles a width x height image covers.
func tiles(width, height int) int {
	return ((width + tileSize - 1) / tileSize) * ((height + tileSize - 1) / tileSize)
}

// fitTileBudget scales width x height down, keeping its aspect ratio, until
// it covers at most maxTiles tiles: scale by area, then round the scaled
// tile grid down so the integer size stays within the budget.
func fitTileBudget(width, height int) (int, int) {
	if tiles(width, height) <= maxTiles {
		return width, height
	}
	w, h := float64(width), float64(height)
	scale := math.Sqrt(tileSize * tileSize * maxTiles / w / h)
	wide, high := w*scale/tileSize, h*scale/tileSize
	scale *= min(math.Floor(wide)/wide, math.Floor(high)/high)
	return max(1, int(math.Floor(w*scale))), max(1, int(math.Floor(h*scale)))
}

func formatDimensionNote(result preparedImageResult) string {
	if !result.WasResized {
		return ""
	}
	scale := float64(result.OriginalWidth) / float64(result.Width)
	return fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]",
		result.OriginalWidth, result.OriginalHeight, result.Width, result.Height, scale)
}

func encodedSizeBase64(data []byte) int {
	return base64.StdEncoding.EncodedLen(len(data))
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// detectFormat sniffs the source format from magic bytes. Returns
// "png" / "jpeg" / "webp" / "gif" / "bmp" / "" (unknown).
func detectFormat(b []byte) string {
	if bytes.HasPrefix(b, pngSignature) {
		return "png"
	}
	if bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) {
		return "jpeg"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "webp"
	}
	if startsWithASCII(b, 0, "GIF87a") || startsWithASCII(b, 0, "GIF89a") {
		return "gif"
	}
	if bytes.HasPrefix(b, []byte{'B', 'M'}) {
		return "bmp"
	}
	return ""
}

func mimeForFormat(f string) string {
	if f == "" {
		return ""
	}
	return "image/" + f
}

func isPNG(buffer []byte) bool {
	return len(buffer) >= 16 && int(binary.BigEndian.Uint32(buffer[len(pngSignature):])) == 13 && startsWithASCII(buffer, 12, "IHDR")
}

func isAnimatedPNG(buffer []byte) bool {
	offset := len(pngSignature)
	for offset+8 <= len(buffer) {
		chunkLength := int(binary.BigEndian.Uint32(buffer[offset:]))
		chunkTypeOffset := offset + 4
		if startsWithASCII(buffer, chunkTypeOffset, "acTL") {
			return true
		}
		if startsWithASCII(buffer, chunkTypeOffset, "IDAT") {
			return false
		}
		nextOffset := offset + 8 + chunkLength + 4
		if nextOffset <= offset || nextOffset > len(buffer) {
			return false
		}
		offset = nextOffset
	}
	return false
}

func startsWithASCII(buffer []byte, offset int, text string) bool {
	return len(buffer) >= offset && bytes.HasPrefix(buffer[offset:], []byte(text))
}
