package tui

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/tui/widthx"
)

type ImageProtocol string

const (
	ImageProtocolKitty  ImageProtocol = "kitty"
	ImageProtocolITerm2 ImageProtocol = "iterm2"
)

type TerminalCapabilities struct {
	Images     ImageProtocol
	TrueColor  bool
	Hyperlinks bool
}

type CellDimensions struct {
	WidthPx  int
	HeightPx int
}

type ImageDimensions struct {
	WidthPx  int
	HeightPx int
}

type ImageRenderOptions struct {
	MaxWidthCells       int
	MaxHeightCells      int
	PreserveAspectRatio bool
	ImageID             int
	Name                string
	MoveCursor          bool // if false, Kitty C=1 suppresses terminal-side cursor movement
}

type renderedImage struct {
	Sequence string
	Rows     int
	ImageID  int
}

// CapabilityOverrides is a partial TerminalCapabilities as passed to
// SetCapabilityOverrides. A nil field is absent. Images points at "" for
// no image protocol.
type CapabilityOverrides struct {
	Images     *ImageProtocol
	TrueColor  *bool
	Hyperlinks *bool
}

func (o CapabilityOverrides) equal(other CapabilityOverrides) bool {
	return ptrValueEqual(o.Images, other.Images) &&
		ptrValueEqual(o.TrueColor, other.TrueColor) &&
		ptrValueEqual(o.Hyperlinks, other.Hyperlinks)
}

func ptrValueEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (o CapabilityOverrides) clone() CapabilityOverrides {
	return CapabilityOverrides{Images: clonePtr(o.Images), TrueColor: clonePtr(o.TrueColor), Hyperlinks: clonePtr(o.Hyperlinks)}
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

var (
	// capabilityMu makes override replacement and cache invalidation one
	// operation. The cache remains atomic because renderers read it often.
	capabilityMu         sync.Mutex
	cachedCapabilities   atomic.Pointer[TerminalCapabilities]
	capabilityOverrides  CapabilityOverrides
	cellDimensions       = CellDimensions{WidthPx: 9, HeightPx: 18}
	tmuxHyperlinkProbe   = probeTmuxHyperlinks
	tmuxProbeTimeout     = 250 * time.Millisecond
	capabilityDetectGOOS = runtime.GOOS
)

func CurrentCellDimensions() CellDimensions { return cellDimensions }
func SetCellDimensions(dims CellDimensions) {
	cellDimensions = dims
}

// probeTmuxHyperlinks reports whether tmux forwards hyperlinks: tmux re-emits OSC 8
// only when the attached client's client_termfeatures lists hyperlinks. Any
// error, including the 250ms timeout, reports false.
func probeTmuxHyperlinks() bool {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_termfeatures}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return tmuxTermfeaturesIncludeHyperlinks(string(out))
}

func tmuxTermfeaturesIncludeHyperlinks(termfeatures string) bool {
	for feature := range strings.SplitSeq(termfeatures, ",") {
		if strings.TrimSpace(feature) == "hyperlinks" {
			return true
		}
	}
	return false
}

// detectCapabilitiesFromEnvironment detects capabilities from the terminal
// environment for the given goos.
func detectCapabilitiesFromEnvironment(tmuxForwardsHyperlink func() bool, goos string) TerminalCapabilities {
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	terminalEmulator := strings.ToLower(os.Getenv("TERMINAL_EMULATOR"))
	term := strings.ToLower(os.Getenv("TERM"))
	colorTerm := strings.ToLower(os.Getenv("COLORTERM"))
	hasTrueColorHint := colorTerm == "truecolor" || colorTerm == "24bit"
	isWindowsConsole := goos == "windows"

	// Require Herdr's explicit image-forwarding signal.
	if os.Getenv("HERDR_ENV") == "1" {
		if os.Getenv("HERDR_KITTY_GRAPHICS") == "1" {
			return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
		}
		return TerminalCapabilities{Images: "", TrueColor: hasTrueColorHint, Hyperlinks: false}
	}
	// Emit OSC 8 hyperlinks only when tmux confirms it forwards. Image
	// protocols are unreliable under tmux, so leave images off.
	if os.Getenv("TMUX") != "" || strings.HasPrefix(term, "tmux") {
		return TerminalCapabilities{Images: "", TrueColor: hasTrueColorHint, Hyperlinks: tmuxForwardsHyperlink()}
	}
	// screen does not forward OSC 8 hyperlinks, so keep them off there.
	if strings.HasPrefix(term, "screen") {
		return TerminalCapabilities{Images: "", TrueColor: hasTrueColorHint, Hyperlinks: false}
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || termProgram == "kitty" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "ghostty" || strings.Contains(term, "ghostty") || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WEZTERM_PANE") != "" || termProgram == "wezterm" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	// Warp supports the Kitty graphics protocol and OSC 8 hyperlinks.
	if termProgram == "warpterminal" || os.Getenv("WARP_SESSION_ID") != "" || os.Getenv("WARP_TERMINAL_SESSION_UUID") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("ITERM_SESSION_ID") != "" || termProgram == "iterm.app" {
		return TerminalCapabilities{Images: ImageProtocolITerm2, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WT_SESSION") != "" {
		return TerminalCapabilities{Images: "", TrueColor: true, Hyperlinks: true}
	}
	// Orca's terminal handles OSC 8 links (it offers the system or its
	// built-in browser on click).
	if termProgram == "alacritty" || termProgram == "vscode" || termProgram == "zed" || termProgram == "orca" {
		return TerminalCapabilities{Images: "", TrueColor: true, Hyperlinks: true}
	}
	if terminalEmulator == "jetbrains-jediterm" {
		return TerminalCapabilities{Images: "", TrueColor: true, Hyperlinks: false}
	}
	// Windows Terminal does not always set WT_SESSION. Modern Windows consoles
	// support truecolor; keep hyperlinks off unless positively detected above.
	if isWindowsConsole {
		return TerminalCapabilities{Images: "", TrueColor: true, Hyperlinks: false}
	}
	// Unknown terminal: be conservative about OSC 8, which terminals that
	// swallow it render as bare text with the URL gone.
	return TerminalCapabilities{Images: "", TrueColor: hasTrueColorHint, Hyperlinks: false}
}

// parseBooleanCapabilityOverride parses an override: only "1" and "0" override.
func parseBooleanCapabilityOverride(value string) *bool {
	switch value {
	case "1":
		return new(true)
	case "0":
		return new(false)
	}
	return nil
}

// DetectCapabilities detects terminal capabilities: WOPR_HYPERLINKS,
// WOPR_IMAGE_PROTOCOL, and WOPR_TRUE_COLOR override auto-detection, and an
// explicit WOPR_HYPERLINKS replaces the tmux probe. A nil tmuxForwardsHyperlink
// uses the default tmux probe.
func DetectCapabilities(tmuxForwardsHyperlink func() bool) TerminalCapabilities {
	if tmuxForwardsHyperlink == nil {
		tmuxForwardsHyperlink = tmuxHyperlinkProbe
	}
	hyperlinks := parseBooleanCapabilityOverride(os.Getenv("WOPR_HYPERLINKS"))
	probe := tmuxForwardsHyperlink
	if hyperlinks != nil {
		probe = func() bool { return *hyperlinks }
	}
	detected := detectCapabilitiesFromEnvironment(probe, capabilityDetectGOOS)
	switch imageProtocol := strings.ToLower(os.Getenv("WOPR_IMAGE_PROTOCOL")); imageProtocol {
	case "kitty", "iterm2":
		detected.Images = ImageProtocol(imageProtocol)
	case "none", "0":
		detected.Images = ""
	}
	if trueColor := parseBooleanCapabilityOverride(os.Getenv("WOPR_TRUE_COLOR")); trueColor != nil {
		detected.TrueColor = *trueColor
	}
	if hyperlinks != nil {
		detected.Hyperlinks = *hyperlinks
	}
	return detected
}

// Capabilities returns detection with the WOPR_*
// environment, then the settings overrides on top.
func Capabilities() TerminalCapabilities {
	capabilityMu.Lock()
	defer capabilityMu.Unlock()
	if cached := cachedCapabilities.Load(); cached != nil {
		return *cached
	}
	overrides := capabilityOverrides
	var probe func() bool
	if overrides.Hyperlinks != nil {
		hyperlinks := *overrides.Hyperlinks
		probe = func() bool { return hyperlinks }
	}
	caps := DetectCapabilities(probe)
	if overrides.Images != nil {
		caps.Images = *overrides.Images
	}
	if overrides.TrueColor != nil {
		caps.TrueColor = *overrides.TrueColor
	}
	if overrides.Hyperlinks != nil {
		caps.Hyperlinks = *overrides.Hyperlinks
	}
	cachedCapabilities.Store(&caps)
	return caps
}

// SetCapabilityOverrides replaces the overrides and drops the cache only when
// a field changed.
func SetCapabilityOverrides(overrides CapabilityOverrides) {
	capabilityMu.Lock()
	defer capabilityMu.Unlock()
	if capabilityOverrides.equal(overrides) {
		return
	}
	capabilityOverrides = overrides.clone()
	cachedCapabilities.Store(nil)
}

// SetCapabilities overrides the cached capabilities.
func SetCapabilities(caps TerminalCapabilities) {
	capabilityMu.Lock()
	cachedCapabilities.Store(&caps)
	capabilityMu.Unlock()
}

// IsImageLine reports whether the line contains Kitty or iTerm2 image protocol bytes.
func IsImageLine(line string) bool { return widthx.IsImageLine(line) }

func EncodeKitty(base64Data string, columns, rows, imageID int, moveCursor ...bool) string {
	const chunkSize = 4096
	params := []string{"a=T", "f=100", "q=2"}
	if columns > 0 {
		params = append(params, fmt.Sprintf("c=%d", columns))
	}
	if rows > 0 {
		params = append(params, fmt.Sprintf("r=%d", rows))
	}
	if imageID > 0 {
		params = append(params, fmt.Sprintf("i=%d", imageID))
	}
	// C=1 suppresses Kitty's built-in cursor movement after placement
	if len(moveCursor) > 0 && !moveCursor[0] {
		params = append(params, "C=1")
	}
	if len(base64Data) <= chunkSize {
		return "\x1b_G" + strings.Join(params, ",") + ";" + base64Data + "\x1b\\"
	}
	var chunks []string
	for off := 0; off < len(base64Data); off += chunkSize {
		end := min(off+chunkSize, len(base64Data))
		chunk := base64Data[off:end]
		switch {
		case off == 0:
			chunks = append(chunks, "\x1b_G"+strings.Join(params, ",")+",m=1;"+chunk+"\x1b\\")
		case end == len(base64Data):
			chunks = append(chunks, "\x1b_Gm=0;"+chunk+"\x1b\\")
		default:
			chunks = append(chunks, "\x1b_Gm=1;"+chunk+"\x1b\\")
		}
	}
	return strings.Join(chunks, "")
}

func DeleteKittyImage(imageID int) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", imageID)
}

func DeleteAllKittyImages() string { return "\x1b_Ga=d,d=A,q=2\x1b\\" }

// DeleteAllKittyPlacements removes every Kitty image placement (visible
// renderings) while leaving the transmitted image data intact.
func DeleteAllKittyPlacements() string { return "\x1b_Ga=d,d=a,q=2\x1b\\" }

func EncodeITerm2(base64Data string, width any, height any, name string, preserveAspect bool) string {
	params := []string{"inline=1"}
	if width != nil {
		params = append(params, fmt.Sprintf("width=%v", width))
	}
	if height != nil {
		params = append(params, fmt.Sprintf("height=%v", height))
	}
	if name != "" {
		params = append(params, "name="+base64.StdEncoding.EncodeToString([]byte(name)))
	}
	if !preserveAspect {
		params = append(params, "preserveAspectRatio=0")
	}
	return "\x1b]1337;File=" + strings.Join(params, ";") + ":" + base64Data + "\x07"
}

type ImageCellSize struct {
	Columns int
	Rows    int
}

func CalculateImageCellSize(imageDimensions ImageDimensions, maxWidthCells int, maxHeightCells int, dims CellDimensions) ImageCellSize {
	maxWidth := max(1, maxWidthCells)
	imageWidth := max(1, imageDimensions.WidthPx)
	imageHeight := max(1, imageDimensions.HeightPx)

	widthScale := float64(maxWidth*dims.WidthPx) / float64(imageWidth)
	heightScale := widthScale
	if maxHeightCells > 0 {
		heightScale = float64(maxHeightCells*dims.HeightPx) / float64(imageHeight)
	}
	scale := min(widthScale, heightScale)

	scaledWidthPx := float64(imageWidth) * scale
	scaledHeightPx := float64(imageHeight) * scale
	columns := max(1, min(maxWidth, int(math.Ceil(scaledWidthPx/float64(dims.WidthPx)))))
	rows := max(1, int(math.Ceil(scaledHeightPx/float64(dims.HeightPx))))
	if maxHeightCells > 0 && rows > maxHeightCells {
		rows = maxHeightCells
	}

	return ImageCellSize{Columns: columns, Rows: rows}
}

// GetImageDimensions reads the pixel size from base64 PNG, JPEG, GIF or WebP
// data, or returns nil when the header does not decode.
func GetImageDimensions(base64Data, _ string) *ImageDimensions {
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(base64Data)))
	if err != nil {
		return nil
	}
	return &ImageDimensions{WidthPx: cfg.Width, HeightPx: cfg.Height}
}

func RenderImage(base64Data string, imageDimensions ImageDimensions, options ImageRenderOptions) *renderedImage {
	caps := Capabilities()
	if caps.Images == "" {
		return nil
	}
	maxWidth := options.MaxWidthCells
	if maxWidth <= 0 {
		maxWidth = 80
	}
	size := CalculateImageCellSize(imageDimensions, maxWidth, options.MaxHeightCells, CurrentCellDimensions())
	switch caps.Images {
	case ImageProtocolKitty:
		if options.ImageID > 0 {
			RegisterKittyImageMetadata(KittyImageMetadata{
				ImageID:  options.ImageID,
				Columns:  size.Columns,
				Rows:     size.Rows,
				WidthPx:  imageDimensions.WidthPx,
				HeightPx: imageDimensions.HeightPx,
			})
		}
		seq := EncodeKitty(base64Data, size.Columns, size.Rows, options.ImageID, options.MoveCursor)
		return &renderedImage{Sequence: seq, Rows: size.Rows, ImageID: options.ImageID}
	case ImageProtocolITerm2:
		preserve := true
		if !options.PreserveAspectRatio {
			preserve = false
		}
		seq := EncodeITerm2(base64Data, size.Columns, "auto", "", preserve)
		return &renderedImage{Sequence: seq, Rows: size.Rows}
	default:
		return nil
	}
}

func Hyperlink(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func ImageFallback(mimeType string, dimensions *ImageDimensions, filename string) string {
	parts := []string{}
	if filename != "" {
		parts = append(parts, filename)
	}
	parts = append(parts, "["+mimeType+"]")
	if dimensions != nil {
		parts = append(parts, fmt.Sprintf("%dx%d", dimensions.WidthPx, dimensions.HeightPx))
	}
	return "[Image: " + strings.Join(parts, " ") + "]"
}
