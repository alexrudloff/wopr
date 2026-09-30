package tui

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// TUI renders an application-owned alternate-screen viewport.
// The caller owns input and lifecycle dispatch.

const (
	altEnterAltScreen           = "\x1b[?1049h"
	altExitAltScreen            = "\x1b[?1049l"
	altDisableAutowrap          = "\x1b[?7l"
	altEnableAutowrap           = "\x1b[?7h"
	altEnableButtonMotionMouse  = "\x1b[?1000h\x1b[?1002h\x1b[?1004h\x1b[?1006h"
	altEnableAllMotionMouse     = "\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1004h\x1b[?1006h"
	altDisableMouse             = "\x1b[?1006l\x1b[?1004l\x1b[?1003l\x1b[?1002l\x1b[?1000l"
	altBeginSynchronizedOutput  = "\x1b[?2026h"
	altEndSynchronizedOutput    = "\x1b[?2026l"
	altFocusIn                  = "\x1b[I"
	altFocusOut                 = "\x1b[O"
	altPageScrollOverlap        = 4
	altMaxCachedOffscreenImages = 16
	altMaxCachedOffscreenTxB    = 32 * 1024 * 1024
	altMaxCachedOffscreenDecB   = 64 * 1024 * 1024
)

// cachedKittyImage tracks a transmitted image whose data is already uploaded so
// the alt-screen can re-place it without re-transmitting.
type cachedKittyImage struct {
	transmissionGeneration int
	transmissionBytes      int
	estimatedDecodedBytes  int
}

// TUI is the fullscreen renderer: it paints the layout root and overlays on
// the terminal's alternate screen, coalesces render requests, and handles
// mouse selection, scrolling, and search.
type TUI struct {
	Container

	out    io.Writer
	width  int
	height int

	forceRedraw        bool // set by ForceFullRender/requestRender(force); cleared after next frame
	fixedSize          bool // true when constructed via NewWithOutput (tests)
	showHardwareCursor bool // WOPR_HARDWARE_CURSOR

	// overlayMu guards the overlays (bottom to top), the keyboard focus
	// target, and where the last frame painted each overlay.
	overlayMu        sync.Mutex
	overlays         []*overlayEntry
	nextOverlayID    overlayID
	focusTarget      Component
	renderedOverlays []renderedOverlay

	// kitty graphics support
	kitty bool

	renderRequested  bool
	renderTimer      stoppableTimer
	renderGeneration uint64
	lastRenderAt     time.Time
	now              func() time.Time
	afterFunc        func(time.Duration, func()) stoppableTimer

	// renderOnMain, when set, marshals a scheduled render onto the owner's
	// main loop instead of running doRender on the throttle-timer
	// goroutine. time.AfterFunc fires on a separate goroutine, so without this
	// hook a scheduled doRender reads the lock-free component tree
	// concurrently with the main loop's mutations (e.g. the working-spinner
	// frame). nil = run inline, which
	// is correct for standalone / single-goroutine use.
	renderOnMain func(render func())

	// tickOnMain, when set, marshals an owned state-machine tick (currently
	// the alt-screen selection auto-scroll) onto the owner's main loop. Its
	// contract differs from renderOnMain: it BLOCKS (backpressures) while the
	// owner loop is alive so a tick is never dropped against a full queue, and
	// returns unenqueued only at owner-loop termination, when the tick is moot.
	// renderOnMain instead drops on a saturated queue even while the loop runs,
	// which would leave the one-shot timer's stale non-nil pointer in place so
	// the tick never re-arms and auto-scroll wedges. Production wires it to a
	// blocking owner-loop post cancelled by the owner-loop context; renderOnMain
	// is deliberately not reused because scheduled renders are cosmetic and
	// droppable. nil = run inline (standalone / single-goroutine use).
	tickOnMain func(func())

	mu sync.Mutex // protects render state

	stopped bool // set by Stop(); prevents further Render() calls

	previousScreen       []string
	lastDocument         []string
	previousScreenWidth  int
	previousScreenHeight int
	layoutRoot           Component
	currentLayout        *LayoutFrame

	implicitDocument   Component
	implicitScrollView *ScrollView
	flashes            *AltScreenFlashContainer

	altScreenActive bool
	imageProtocol   ImageProtocol

	// savedCapabilities holds the pre-enter terminal capabilities when the
	// alt-screen suppressed iTerm2 inline images (they do not render correctly in
	// the alternate buffer); restored on exit.
	savedCapabilities *TerminalCapabilities

	// uploadedKittyImages caches transmitted-image accounting keyed by image id
	// so offscreen images are re-placed rather than re-transmitted and evicted
	// once the offscreen budget is exceeded.
	uploadedKittyImages      map[int]cachedKittyImage
	uploadedKittyImagesOrder []int

	fullRedrawCount int

	wheelScrollLines int
	mouseEnabled     bool
	copyOnSelect     bool
	copySelection    func(text string) error
	openURL          func(url string)

	searchMatchStyle            func(text string) string
	searchCurrentMatchStyle     func(text string) string
	searchNavigationButtonStyle func(text string, hovered bool) string
	scrollToEndIndicator        func() string
	background                  func() string
	backdrop                    func() (float64, string)
	onRightClickPaste           func()

	// activeSearch is the open transcript search (nil when closed); guarded
	// by t.mu. scrollToEndIndicatorRect is where the last frame painted the
	// jump-to-end label.
	activeSearch             *altActiveSearch
	scrollToEndIndicatorRect *scrollToEndIndicatorRect

	// flash shows a transient message; it is Flash, replaceable in tests.
	flash func(message string, durationMs int)

	// renderingFrame is set while doRender holds t.mu; a render requested by
	// a scroll view during the frame is recorded in renderRequestedInFrame
	// and issued after the lock is released.
	renderingFrame         atomic.Bool
	renderRequestedInFrame atomic.Bool

	// Component mouse gesture state, owner-loop only (see
	// renderer_mouse.go).
	mouseCapture       *TuiMouseDispatchTarget
	mousePressTarget   *TuiMouseDispatchTarget
	mousePressPoint    *pointerXY
	mousePressMoved    bool
	lastComponentClick *componentClick

	// Selection state populated by the mouse handlers (layer 7c-B). anchor/focus
	// bound the application-owned text selection; applySelection reads them under
	// t.mu during doRender.
	selectionAnchor *selectionPoint
	selectionFocus  *selectionPoint
	// selectionGranularity is "character", or "word"/"line" after a double or
	// triple click, which snaps drags to selectionInitialRange's units.
	selectionGranularity  string
	selectionInitialRange *selectionRange
	lastClick             *clickTarget

	// Selection drag / auto-scroll state (layer 7c-B2). All mutated under t.mu.
	// selectionAutoScrollTimer is an owned re-arming timer; it is stopped on
	// selection end and in StopWithOptions so no goroutine leaks.
	selectionDragPointer     *pointerXY
	selectionAutoScrollDir   int
	selectionAutoScrollTimer stoppableTimer
	// selectionAutoScrollGen invalidates in-flight auto-scroll callbacks. Any
	// stop bumps it, so a tick that unlocked to ScrollBy and then reacquires the
	// lock revalidates its captured generation before mutating focus or
	// re-arming; a release/focus-out/stop that ran during the scroll makes the
	// stale callback bail instead of resurrecting cleared selection state.
	selectionAutoScrollGen uint64
	selectionPressActive   bool
	selectionDragged       bool
	pressedURL             string
	pressedURLSet          bool
	scrollbarDrag          *scrollbarDrag
	scrollbarHover         *ScrollView
	// pendingScrollbarActivity queues hover transitions decided under t.mu.
	// ScrollView.SetScrollbarActive requests a render, which locks t.mu, so the
	// transitions apply after the lock is released (unlockAndApplyHover).
	pendingScrollbarActivity []scrollbarActivity
}

// Options configures the renderer.
type Options struct {
	// WheelScrollLines is the number of logical lines moved per wheel event
	// (default 1).
	WheelScrollLines int
	// Mouse captures mouse events for viewport scrolling and selection.
	Mouse *bool
	// CopyOnSelect copies a completed text selection. The default is true.
	CopyOnSelect *bool
	// CopySelection writes selected text to the host clipboard. OSC 52 is used when nil.
	CopySelection func(text string) error
	// OpenURL activates an OSC 8 hyperlink on primary-button click.
	OpenURL func(url string)
	// SearchMatchStyle styles a non-current transcript search match.
	SearchMatchStyle func(text string) string
	// SearchCurrentMatchStyle styles the current transcript search match.
	SearchCurrentMatchStyle func(text string) string
	// SearchNavigationButtonStyle styles a transcript search navigation button.
	SearchNavigationButtonStyle func(text string, hovered bool) string
	// ScrollToEndIndicator renders a clickable jump-to-end label, centered on
	// the last row of a follow-end primary scroll view while that view is
	// scrolled away from its end.
	ScrollToEndIndicator func() string
	// OnRightClickPaste handles an unmodified secondary-button press for
	// clipboard paste. Currently enabled on Windows only.
	OnRightClickPaste func()
	// Background returns the SGR background painted under every screen cell
	// that has none of its own; "" leaves the terminal default. It is read on
	// every frame so a theme change repaints the screen.
	Background func() string
}

// altScreenDocument wraps the base component tree so it can be placed inside the
// implicit scroll view.
type altScreenDocument struct {
	base *TUI
}

func (d *altScreenDocument) Render(width int) []string { return d.base.Container.Render(width) }
func (d *altScreenDocument) renderBorrowed(width int) []string {
	return d.base.renderBorrowed(width)
}

// HandleMouse dispatches to the base children.
func (d *altScreenDocument) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	return d.base.HandleMouse(event)
}

func (d *altScreenDocument) Invalidate() {
	d.base.mu.Lock()
	children := append([]Component(nil), d.base.children...)
	d.base.mu.Unlock()
	for _, child := range children {
		child.Invalidate()
	}
}

func newTUI(out io.Writer, showHardwareCursor bool, options Options) *TUI {
	copyOnSelect := options.CopyOnSelect == nil || *options.CopyOnSelect
	t := &TUI{
		out:                out,
		showHardwareCursor: showHardwareCursor,
		now:                time.Now,
		afterFunc: func(d time.Duration, fn func()) stoppableTimer {
			return time.AfterFunc(d, fn)
		},
		uploadedKittyImages: map[int]cachedKittyImage{},
		wheelScrollLines:    max(1, options.WheelScrollLines),
		mouseEnabled:        options.Mouse == nil || *options.Mouse,
		copyOnSelect:        copyOnSelect,
		copySelection:       options.CopySelection,
		openURL:             options.OpenURL,

		searchMatchStyle:            options.SearchMatchStyle,
		searchCurrentMatchStyle:     options.SearchCurrentMatchStyle,
		searchNavigationButtonStyle: options.SearchNavigationButtonStyle,
		scrollToEndIndicator:        options.ScrollToEndIndicator,
		background:                  options.Background,
		onRightClickPaste:           options.OnRightClickPaste,
		selectionGranularity:        selectCharacter,
	}
	if t.searchMatchStyle == nil {
		t.searchMatchStyle = func(text string) string { return "\x1b[4m" + text + "\x1b[24m" }
	}
	if t.searchCurrentMatchStyle == nil {
		t.searchCurrentMatchStyle = func(text string) string { return "\x1b[1;7m" + text + "\x1b[22;27m" }
	}
	if t.searchNavigationButtonStyle == nil {
		t.searchNavigationButtonStyle = func(text string, _ bool) string { return text }
	}
	t.flash = t.Flash
	t.implicitDocument = &altScreenDocument{base: t}
	t.implicitScrollView = NewScrollView(t.implicitDocument, ScrollViewOptions{Follow: "end", Primary: true})
	t.flashes = NewAltScreenFlashContainer(func() { t.RequestRender() })
	return t
}

// New creates a renderer writing to stdout at the current terminal size.
func New(options Options) *TUI {
	t := newTUI(os.Stdout, os.Getenv("WOPR_HARDWARE_CURSOR") == "1", options)
	t.updateSize()
	t.kitty = detectKitty()
	return t
}

// NewWithOutput creates a fixed-size renderer for tests.
func NewWithOutput(out io.Writer, cols, rows int, options Options) *TUI {
	t := newTUI(out, os.Getenv("WOPR_HARDWARE_CURSOR") == "1", options)
	t.width = cols
	t.height = rows
	t.fixedSize = true
	return t
}

// ViewportTop returns the primary scroll view's current scroll offset.
func (t *TUI) ViewportTop() int { return t.getPrimaryScrollView().ScrollTop() }

// IsFollowingOutput reports whether the primary scroll view is pinned to the end.
func (t *TUI) IsFollowingOutput() bool { return t.getPrimaryScrollView().IsFollowingEnd() }

// CopyOnSelect reports whether a completed selection is copied automatically.
func (t *TUI) CopyOnSelect() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.copyOnSelect
}

// SetCopyOnSelect changes automatic selection copy without rebuilding the renderer.
// SetWheelScrollLines sets how many lines one wheel step scrolls.
func (t *TUI) SetWheelScrollLines(lines int) {
	t.mu.Lock()
	t.wheelScrollLines = max(1, lines)
	t.mu.Unlock()
}

func (t *TUI) SetCopyOnSelect(enabled bool) {
	t.mu.Lock()
	t.copyOnSelect = enabled
	t.mu.Unlock()
}

// HasActiveSelection reports whether the fullscreen viewport has selected text.
func (t *TUI) HasActiveSelection() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _, ok := t.getSelectionBounds()
	return ok
}

// CopyActiveSelectionToClipboard copies the current selection and reports success.
func (t *TUI) CopyActiveSelectionToClipboard() bool {
	t.mu.Lock()
	text, ok := t.activeSelectionTextLocked()
	t.mu.Unlock()
	if !ok {
		return false
	}
	return t.copyTextToClipboard(text)
}

// SetLayoutRoot installs the fullscreen layout tree (transcript scroll view +
// pinned footer). nil falls back to the implicit scroll view wrapping the base
// document.
func (t *TUI) SetLayoutRoot(component Component) {
	t.mu.Lock()
	if t.layoutRoot == component {
		t.mu.Unlock()
		return
	}
	t.layoutRoot = component
	t.currentLayout = nil
	t.mu.Unlock()
	t.RequestRender()
}

func (t *TUI) getPrimaryScrollView() *ScrollView {
	if t.currentLayout != nil && t.currentLayout.PrimaryScrollView != nil {
		return t.currentLayout.PrimaryScrollView
	}
	return t.implicitScrollView
}

// Start enters the alternate screen and begins rendering. The driver calls this
// once at session start.
func (t *TUI) Start() {
	t.mu.Lock()
	// Clear stopped first, then reset the selection/scrollbar input state so a
	// start->stop->start cycle resumes with a clean slate and rendering
	// (doRender gates on stopped).
	t.stopped = false
	t.stopScrollbarHover()
	t.stopScrollbarDrag()
	t.clearTextSelectionLocked()
	t.lastClick = nil
	t.flashes.Dispose()
	t.altScreenActive = true
	caps := Capabilities()
	t.imageProtocol = caps.Images
	clear(t.uploadedKittyImages)
	t.uploadedKittyImagesOrder = nil
	t.lastDocument = nil
	t.resetRenderStateLocked()
	// iTerm2 inline images do not render in the alternate buffer; suppress them
	// for the alt-screen lifetime and restore on exit. kitty images work in the
	// alt buffer and are kept.
	suppressITerm2 := caps.Images == ImageProtocolITerm2
	if suppressITerm2 {
		saved := caps
		t.savedCapabilities = &saved
	}
	mouse := ""
	if t.mouseEnabled {
		mouse = altMouseEnableSequence()
	}
	t.unlockAndApplyHover()
	t.clearComponentMouseGesture()
	t.lastComponentClick = nil
	if suppressITerm2 {
		suppressed := caps
		suppressed.Images = ""
		SetCapabilities(suppressed)
		t.invalidateMountedRoots()
	}
	_, _ = fmt.Fprint(t.out, altEnterAltScreen+altDisableAutowrap+mouse+"\x1b[2J\x1b[H\x1b[?25l")
	t.QueryCellSize()
	t.Render()
}

// getMountedRoots returns the layout root when set, else the base children.
func (t *TUI) getMountedRoots() []Component {
	t.mu.Lock()
	root := t.layoutRoot
	t.mu.Unlock()
	if root != nil {
		return []Component{root}
	}
	return t.childSnapshot()
}

// invalidateMountedRoots invalidates the rendered document tree so cached lines
// are recomputed after a capability change: layoutRoot when set, else the
// base children.
func (t *TUI) invalidateMountedRoots() {
	t.mu.Lock()
	root := t.layoutRoot
	t.mu.Unlock()
	if root != nil {
		root.Invalidate()
		return
	}
	t.implicitDocument.Invalidate()
}

// StopOptions controls alt-screen teardown.
type StopOptions struct {
	// PreserveScreen leaves the alternate screen contents visible on exit
	// instead of printing the final document to the main screen.
	PreserveScreen bool
	// Clear, with PreserveScreen, clears the main screen after leaving the
	// alternate screen, so the shell prompt starts at the top.
	Clear bool
}

// Stop leaves the alternate screen and prints the document to the main
// screen; StopWithOptions can preserve the screen instead.
func (t *TUI) Stop() { t.StopWithOptions(StopOptions{}) }

// StopWithOptions tears down the alternate screen.
func (t *TUI) StopWithOptions(options StopOptions) {
	t.closeSearch()
	t.clearComponentMouseGesture()
	t.mu.Lock()
	t.stopped = true
	if t.renderTimer != nil {
		t.renderTimer.Stop()
		t.renderTimer = nil
	}
	t.stopSelectionAutoScrollLocked()
	// Clear active press, hover, and drag so no stale input state survives
	// teardown.
	t.selectionPressActive = false
	t.stopScrollbarHover()
	t.stopScrollbarDrag()
	t.flashes.Dispose()
	t.implicitScrollView.Dispose()
	if !t.altScreenActive {
		t.unlockAndApplyHover()
		return
	}
	mouse := ""
	if t.mouseEnabled {
		mouse = altDisableMouse
	}
	_, _ = fmt.Fprint(t.out, altBeginSynchronizedOutput+t.deleteKittyImages()+mouse+altEnableAutowrap+altEndSynchronizedOutput)
	clear(t.uploadedKittyImages)
	t.uploadedKittyImagesOrder = nil
	t.altScreenActive = false
	var restore *TerminalCapabilities
	if t.savedCapabilities != nil {
		restore = t.savedCapabilities
		t.savedCapabilities = nil
	}
	width := max(1, t.width)
	if options.PreserveScreen {
		clearMain := ""
		if options.Clear {
			clearMain = "\x1b[H\x1b[2J"
		}
		_, _ = fmt.Fprint(t.out, altBeginSynchronizedOutput+altExitAltScreen+clearMain+"\x1b[?25h"+altEndSynchronizedOutput)
		t.unlockAndApplyHover()
		if restore != nil {
			SetCapabilities(*restore)
		}
		return
	}
	document := t.renderDocument(width)
	var buf strings.Builder
	buf.WriteString(altBeginSynchronizedOutput + altExitAltScreen + altDisableAutowrap)
	for row := range document {
		if row > 0 {
			buf.WriteString("\r\n")
		}
		buf.WriteString("\r\x1b[2K" + document[row])
	}
	buf.WriteString("\x1b[0m" + altEnableAutowrap + "\r\n\x1b[?25h" + altEndSynchronizedOutput)
	t.lastDocument = document
	t.unlockAndApplyHover()
	_, _ = fmt.Fprint(t.out, buf.String())
	// Restore suppressed iTerm2 capabilities after the exit document is written
	// (the dump renders with images suppressed).
	if restore != nil {
		SetCapabilities(*restore)
	}
}

// renderDocument produces the exit-time document lines at the document's natural
// height (not clamped to the terminal), so quitting fullscreen dumps the whole
// transcript into the main-screen scrollback rather than only the visible
// viewport. It renders the layout root (or base children) with OSC 133 zone
// marks and cursor markers stripped, line resets applied, and over-wide lines
// clamped. Caller
// holds t.mu.
func (t *TUI) renderDocument(width int) []string {
	var frameLines []string
	if t.layoutRoot != nil {
		frameLines = t.layoutRoot.Render(width)
	} else {
		frameLines = t.Container.Render(width)
	}
	lines := make([]string, len(frameLines))
	for i, line := range frameLines {
		lines[i] = strings.ReplaceAll(stripOsc133ZonePrefix(line), widthx.CursorMarker, "")
	}
	lines = widthx.ApplyLineResets(lines)
	for i, line := range lines {
		if !widthx.IsImageLine(line) && widthx.VisibleWidth(line) > width {
			lines[i] = widthx.SliceByColumn(line, 0, width, true)
		}
	}
	return lines
}

func (t *TUI) resetRenderStateLocked() {
	t.previousScreen = nil
	t.previousScreenWidth = 0
	t.previousScreenHeight = 0
	t.currentLayout = nil
}

func (t *TUI) rootComponent() Component {
	if t.layoutRoot != nil {
		return t.layoutRoot
	}
	return t.implicitScrollView
}

// ScrollBy scrolls the primary scroll view by the given number of lines.
func (t *TUI) ScrollBy(lines int) {
	t.getPrimaryScrollView().ScrollBy(lines)
	t.RequestRender()
}

// ScrollToTop scrolls the primary scroll view to its start.
func (t *TUI) ScrollToTop() {
	t.getPrimaryScrollView().ScrollToStart()
	t.RequestRender()
}

// ScrollToBottom scrolls the primary scroll view to its end.
func (t *TUI) ScrollToBottom() {
	t.getPrimaryScrollView().ScrollToEnd()
	t.RequestRender()
}

// Flash shows a transient message in the alternate-screen flash stack.
func (t *TUI) Flash(message string, durationMs int) {
	t.flashes.Flash(message, durationMs)
}

// ForceFullRender marks the next frame as a full redraw by resetting the
// render state: a frame with no previous screen repaints everything.
func (t *TUI) ForceFullRender() {
	if t.stopped {
		return
	}
	t.mu.Lock()
	t.resetRenderStateLocked()
	t.mu.Unlock()
	t.RequestRender()
}

// RepaintAll forces an immediate full repaint. Mirrors TUI.RepaintAll (which
// clears hasRendered then renders); the alt-screen clears its differential state.
func (t *TUI) RepaintAll() {
	t.mu.Lock()
	t.resetRenderStateLocked()
	t.mu.Unlock()
	t.Render()
}

// RenderSnapshot returns the rendered document lines at the given width. Mirrors
// TUI.RenderSnapshot; the alt-screen renders the layout root (or base children)
// at natural height.
func (t *TUI) RenderSnapshot(width int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.layoutRoot != nil {
		return t.layoutRoot.Render(width)
	}
	return t.Container.Render(width)
}

// SetShowHardwareCursor toggles the hardware cursor and repaints if already
// rendered so the next frame emits the correct cursor visibility. Mirrors
// TUI.SetShowHardwareCursor.
func (t *TUI) SetShowHardwareCursor(enabled bool) {
	t.mu.Lock()
	if t.showHardwareCursor == enabled {
		t.mu.Unlock()
		return
	}
	t.showHardwareCursor = enabled
	hasRendered := len(t.previousScreen) > 0
	t.mu.Unlock()
	if !enabled {
		t.HideCursor()
	}
	if hasRendered {
		t.Render()
	}
}

func (t *TUI) deleteKittyImages() string {
	if t.imageProtocol == "kitty" {
		return DeleteAllKittyImages()
	}
	return ""
}

// compositeFlashes overlays the transient flash stack onto the bottom rows.
func (t *TUI) compositeFlashes(screen []string, width, height int) []string {
	flashLines := t.flashes.Render(width)
	if len(flashLines) > height {
		flashLines = flashLines[len(flashLines)-height:]
	}
	if len(flashLines) == 0 {
		return screen
	}
	result := append([]string(nil), screen...)
	for len(result) < height {
		result = append(result, "")
	}
	for row := range flashLines {
		line := flashLines[row]
		flashWidth := widthx.VisibleWidth(line)
		if flashWidth == 0 {
			continue
		}
		result[row] = compositeTuiLine(result[row], line, width-flashWidth, flashWidth, width)
	}
	return result
}

// prepareKittyScreen substitutes placement-only commands for already-uploaded
// images and evicts offscreen images past the cache budget. Caller holds t.mu.
func (t *TUI) prepareKittyScreen(screen []string) (lines []string, evictedImageDeletion string) {
	visibleImageIDs := map[int]bool{}
	lines = make([]string, len(screen))
	for i, line := range screen {
		placement, ok := GetKittyImagePlacement(line)
		if !ok {
			lines[i] = line
			continue
		}
		visibleImageIDs[placement.ImageID] = true
		cached, hadCached := t.uploadedKittyImages[placement.ImageID]
		if hadCached {
			t.removeUploadedKittyOrder(placement.ImageID)
		}
		t.uploadedKittyImages[placement.ImageID] = cachedKittyImage{
			transmissionGeneration: placement.TransmissionGeneration,
			transmissionBytes:      placement.TransmissionBytes,
			estimatedDecodedBytes:  placement.EstimatedDecodedBytes,
		}
		t.uploadedKittyImagesOrder = append(t.uploadedKittyImagesOrder, placement.ImageID)
		if hadCached && cached.transmissionGeneration == placement.TransmissionGeneration {
			lines[i] = placement.ReplacementLine
		} else {
			lines[i] = line
		}
	}

	offscreenCount, offscreenTxB, offscreenDecB := 0, 0, 0
	for imageID, cached := range t.uploadedKittyImages {
		if visibleImageIDs[imageID] {
			continue
		}
		offscreenCount++
		offscreenTxB += cached.transmissionBytes
		offscreenDecB += cached.estimatedDecodedBytes
	}

	for _, imageID := range append([]int(nil), t.uploadedKittyImagesOrder...) {
		if offscreenCount <= altMaxCachedOffscreenImages &&
			offscreenTxB <= altMaxCachedOffscreenTxB &&
			offscreenDecB <= altMaxCachedOffscreenDecB {
			break
		}
		if visibleImageIDs[imageID] {
			continue
		}
		cached, ok := t.uploadedKittyImages[imageID]
		if !ok {
			continue
		}
		evictedImageDeletion += DeleteKittyImage(imageID)
		delete(t.uploadedKittyImages, imageID)
		t.removeUploadedKittyOrder(imageID)
		offscreenCount--
		offscreenTxB -= cached.transmissionBytes
		offscreenDecB -= cached.estimatedDecodedBytes
	}
	return lines, evictedImageDeletion
}

func (t *TUI) removeUploadedKittyOrder(imageID int) {
	for i, id := range t.uploadedKittyImagesOrder {
		if id == imageID {
			t.uploadedKittyImagesOrder = append(t.uploadedKittyImagesOrder[:i], t.uploadedKittyImagesOrder[i+1:]...)
			return
		}
	}
}

// requestRenderFromLayout is the render callback handed to scroll views. A
// request made while doRender holds t.mu (a search reveal scrolling the
// transcript) is deferred until the frame releases the lock.
func (t *TUI) requestRenderFromLayout() {
	if t.renderingFrame.Load() {
		t.renderRequestedInFrame.Store(true)
		return
	}
	t.RequestRender()
}

func (t *TUI) doRender() {
	t.mu.Lock()
	t.renderingFrame.Store(true)
	defer func() {
		t.renderingFrame.Store(false)
		t.mu.Unlock()
		if t.renderRequestedInFrame.Swap(false) {
			t.RequestRender()
		}
	}()
	if t.stopped || !t.altScreenActive {
		return
	}
	t.lastRenderAt = t.now()
	t.updateSize()
	width := max(1, t.width)
	height := max(1, t.height)

	root := t.rootComponent()
	nextLayout := RenderLayoutFrame(root, width, height, t.requestRenderFromLayout)
	if t.refreshSearch(&nextLayout) {
		nextLayout = RenderLayoutFrame(root, width, height, t.requestRenderFromLayout)
	}
	screen := make([]string, len(nextLayout.Lines))
	for i, line := range nextLayout.Lines {
		screen[i] = stripOsc133ZonePrefix(line)
	}
	screen = t.applySearchHighlights(screen, &nextLayout)
	screen = t.compositeScrollToEndIndicator(screen, &nextLayout, width)
	screen = t.paintBackdrop(screen, width, height)
	screen = t.compositeOverlays(screen, width, height)
	if len(screen) > height {
		screen = screen[len(screen)-height:]
	}
	screen = t.applySelection(screen, &nextLayout)
	screen = t.compositeFlashes(screen, width, height)

	cursorPos, hasCursor := widthx.ExtractCursorPosition(screen, height)
	screen = widthx.ApplyLineResets(screen)
	for i, line := range screen {
		if !widthx.IsImageLine(line) && widthx.VisibleWidth(line) > width {
			screen[i] = widthx.SliceByColumn(line, 0, width, true)
		}
	}
	if t.background != nil {
		if bg := t.background(); bg != "" {
			for len(screen) < height {
				screen = append(screen, "")
			}
			for i, line := range screen {
				screen[i] = FillBackground(line, width, bg)
			}
		}
	}

	fullRedraw := len(t.previousScreen) == 0 || t.previousScreenWidth != width || t.previousScreenHeight != height
	imagesNeedRedraw := false
	for row, line := range screen {
		var prev string
		if row < len(t.previousScreen) {
			prev = t.previousScreen[row]
		}
		if line != prev && (widthx.IsImageLine(line) || widthx.IsImageLine(prev)) {
			imagesNeedRedraw = true
			break
		}
	}
	redrawImages := fullRedraw || imagesNeedRedraw
	hadUploadedKittyImages := len(t.uploadedKittyImages) > 0

	preparedLines := screen
	evictedImageDeletion := ""
	if redrawImages && t.imageProtocol == "kitty" {
		preparedLines, evictedImageDeletion = t.prepareKittyScreen(screen)
	}

	var buf strings.Builder
	buf.WriteString(altBeginSynchronizedOutput)
	switch {
	case fullRedraw:
		t.fullRedrawCount++
		clearImages := t.deleteKittyImages()
		if t.imageProtocol == "kitty" && hadUploadedKittyImages {
			clearImages = DeleteAllKittyPlacements()
		}
		buf.WriteString(clearImages + "\x1b[2J")
	case imagesNeedRedraw:
		switch t.imageProtocol {
		case "iterm2":
			buf.WriteString("\x1b[2J")
		case "kitty":
			buf.WriteString(DeleteAllKittyPlacements())
		}
	}
	buf.WriteString(evictedImageDeletion)

	// WezTerm erases intersecting Kitty image cells when a later EL clears a
	// covered row. Only separate clearing from drawing for WezTerm frames that
	// place images; text-only frames and other terminals keep the interleaved
	// output.
	clearRowsBeforeKittyImages := redrawImages && t.imageProtocol == "kitty" &&
		slices.ContainsFunc(screen, widthx.IsImageLine) && isWezTermSession()
	repaintAll := fullRedraw || imagesNeedRedraw
	if clearRowsBeforeKittyImages {
		for row := range height {
			if repaintAll || altScreenRow(screen, row) != altScreenRow(t.previousScreen, row) {
				fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", row+1)
			}
		}
	}
	eraseLine := "\x1b[2K"
	if clearRowsBeforeKittyImages {
		eraseLine = ""
	}
	for row := range height {
		if !repaintAll && altScreenRow(screen, row) == altScreenRow(t.previousScreen, row) {
			continue
		}
		fmt.Fprintf(&buf, "\x1b[%d;1H%s%s", row+1, eraseLine, altScreenRow(preparedLines, row))
	}

	if hasCursor {
		fmt.Fprintf(&buf, "\x1b[%d;%dH", cursorPos.Row+1, min(width, cursorPos.Col)+1)
		if t.showHardwareCursor {
			buf.WriteString("\x1b[?25h")
		} else {
			buf.WriteString("\x1b[?25l")
		}
	} else {
		buf.WriteString("\x1b[?25l")
	}
	buf.WriteString(altEndSynchronizedOutput)
	_, _ = fmt.Fprint(t.out, buf.String())

	t.previousScreen = screen
	t.previousScreenWidth = width
	t.previousScreenHeight = height
	t.currentLayout = &nextLayout
}

// SetBackdrop installs the dimming applied under overlays: dim returns the
// opacity of the black cover (0 for none) and the default text color's hex.
// It is read every frame.
func (t *TUI) SetBackdrop(dim func() (opacity float64, textHex string)) {
	t.mu.Lock()
	t.backdrop = dim
	t.mu.Unlock()
}

// paintBackdrop fills the screen background and dims it when a backdrop is
// active, before overlays are composited over it.
func (t *TUI) paintBackdrop(screen []string, width, height int) []string {
	if t.backdrop == nil {
		return screen
	}
	opacity, textHex := t.backdrop()
	if opacity <= 0 {
		return screen
	}
	bg := ""
	if t.background != nil {
		bg = t.background()
	}
	for len(screen) < height {
		screen = append(screen, "")
	}
	for i, line := range screen {
		if bg != "" {
			line = FillBackground(widthx.SliceByColumn(line, 0, width, true), width, bg)
		}
		screen[i] = DimLine(line, opacity, textHex)
	}
	return screen
}

// altScreenRow returns screen[row], or "" past the end.
func altScreenRow(screen []string, row int) string {
	if row < len(screen) {
		return screen[row]
	}
	return ""
}

// isWezTermSession reports whether the process runs inside WezTerm, read per
// frame.
func isWezTermSession() bool {
	return os.Getenv("WEZTERM_PANE") != "" || strings.ToLower(os.Getenv("TERM_PROGRAM")) == "wezterm"
}

// compositeOverlays uses the same state snapshot, geometry, and terminal-cell
// compositor as the regular renderer.
func (t *TUI) compositeOverlays(screen []string, width, height int) []string {
	return t.composeOverlayLines(screen, width, height)
}
