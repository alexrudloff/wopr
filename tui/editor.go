package tui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/alexrudloff/wopr/tui/widthx"
)

var pasteMarkerRegex = regexp.MustCompile(`\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]`)
var pasteMarkerSingle = regexp.MustCompile(`^\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]$`)
var pasteCtrlCSIU = regexp.MustCompile(`\x1b\[(\d+);5u`)

const attachmentAutocompleteDebounce = 20 * time.Millisecond

// Editor is a multi-line text input with undo/kill-ring support.
type Editor struct {
	invalidatable
	// EmbedWorkingStatus opts into the coding-agent status border.
	EmbedWorkingStatus     bool
	panel                  *PromptPanel
	workingStatusIndicator *StatusIndicator
	// Focused is the Focusable state. When true, Render emits widthx.CursorMarker at the cursor position so
	// TUI can place the hardware cursor for IME candidate windows.
	Focused    bool
	lines      []string
	cursor     [2]int // [line, col]
	jumpMode   string // "forward" | "backward" | ""
	history    []editorState
	histIdx    int
	killRing   KillRing
	lastAction string // "kill" | "yank" | "": tracks consecutive ops for accumulate/yank-pop
	yankStart  [2]int // cursor position where last yank was inserted [line, col]
	yankLen    int    // byte length of the last yanked text (single-line yanks only)

	preferredVisualCol   *int
	snappedFromCursorCol *int

	// Slash-command autocomplete suggestions.
	autocomplete      AutocompleteProvider
	autocompleteItems []AutocompleteItem
	// The @-file search runs on a worker: each refreshAutocomplete cancels
	// the one in flight (asyncCancel) and starts a fresh one, whose result is
	// posted through scheduleAsyncApply to the host's main loop. The worker
	// never touches the lock-free editor state itself.
	asyncCancel                   context.CancelFunc
	asyncSeq                      uint64
	scheduleAsyncApply            func(func())
	autocompleteRequestID         uint64
	autocompleteCursor            int    // selected row index
	autocompletePrefix            string // buffer slice the popup is matching against
	autocompleteMousePressedIndex *int
	// autocompleteQueryCursor is the text cursor at the moment
	// autocompleteItems/autocompletePrefix were last populated. The local
	// and async suggestion applies are themselves deferred (scheduleAsyncApply
	// posts them to run on the host's main loop, single-threaded with
	// keystroke handling, so a fast keystroke burst can dispatch more inserts
	// -- including Enter -- before an already-computed apply for an earlier
	// keystroke actually runs). AutocompleteAccept compares this against the
	// live cursor before trusting autocompletePrefix's length, so an accept
	// that arrives after the buffer moved on treats the popup as stale
	// instead of splicing the suggestion's full value into the wrong offset.
	autocompleteQueryCursor [2]int
	autocompleteMax         int // max visible rows (default 5)
	paddingX                int

	// Thinking-level border color: the thinking level maps to a per-level
	// theme color. "off" uses borderMuted.
	// Bash mode overrides this (bashHeaderColor takes precedence).
	ThinkingLevel string // "off" | "low" | "medium" | "high"

	// Submission history for Up/Down arrow navigation.
	// Distinct from the undo history (editorState stack above).
	//
	// inputHistory stores submitted texts, newest first (index 0 = most recent).
	// inputHistIdx is the current browse position: -1 = not browsing.
	// inputHistSaved holds the editor state captured when browsing starts,
	// restored when the user navigates back past index 0.
	inputHistory   []string
	inputHistIdx   int // -1 = live, ≥0 = browsing
	inputHistSaved *editorHistoryDraft

	// Bracketed paste state: buffers content between \x1b[200~ and \x1b[201~.
	isInPaste   bool
	pasteBuffer string

	// Compact paste markers. When a paste is >10 lines or >1000 chars, the editor inserts a marker
	// like "[paste #N +K lines]" and stashes the actual content keyed by N.
	// On submit / GetExpandedText, markers are expanded back to their content.
	pastes       map[int]string
	pasteCounter int

	// renderWidth is the last width passed to Render; used for visual-line movement.
	renderWidth                int
	renderedVisibleLineCount   int
	renderedAutocompleteHeight int
	// Row and column offsets of the last render, used to map mouse events:
	// the first autocomplete row, the first content row, and the content's
	// first column.
	renderedAutocompleteTop int
	renderedContentTop      int
	renderedContentX        int

	// wrapCache memoizes per-logical-line word wrapping (see wrappedChunks).
	// wrapCacheLines is a content snapshot of e.lines at cache time and
	// wrapCacheChunks the matching wrapped chunks, keyed by wrapCacheWidth, so an
	// unchanged line is not re-wrapped every render/keystroke. The expensive
	// grapheme segmentation + wrapping dominates editor render cost on large
	// pasted buffers, where it otherwise runs over the whole buffer twice per
	// render (layoutText + buildVisualLineMap).
	wrapCacheWidth  int
	wrapCacheLines  []string
	wrapCacheChunks [][]textChunk

	// Scroll offset + visible-line cap. When the visual layout exceeds
	// maxVisibleLines, the editor renders a window plus
	// "─── ↑/↓ N more " indicators in the top/bottom borders.
	// maxVisibleLines defaults to 5: interactive mode updates it from
	// `max(5, floor(terminalRows * 0.3))` on resize.
	scrollOffset    int
	maxVisibleLines int

	// Public hooks / flags.
	OnSubmit      func(string)
	OnChange      func(string)
	DisableSubmit bool
}

type editorState struct {
	lines  []string
	cursor [2]int
	// pastes/pasteCounter travel with the snapshot so undo restores the
	// paste registry alongside the text.
	pastes       map[int]string
	pasteCounter int
}

type editorHistoryDraft struct {
	lines  []string
	cursor [2]int
}

type editorVisualLine struct {
	logicalLine int
	startCol    int
	length      int
}

type textChunk struct {
	text       string
	startIndex int
	endIndex   int
}

type layoutLine struct {
	text      string
	hasCursor bool
	cursorPos int
}

func NewEditor() *Editor {
	e := &Editor{lines: []string{""}, inputHistIdx: -1, maxVisibleLines: 5, renderWidth: 80, wrapCacheWidth: -1}
	e.saveHistory()
	return e
}

// SetMaxVisibleLines updates the editor's maximum visible visual-line
// count (set from `max(5, floor(terminalRows * 0.3))` on resize).
// Clamped to ≥ 1.
func (e *Editor) SetMaxVisibleLines(n int) {
	n = max(n, 1)
	if e.maxVisibleLines == n {
		return
	}
	e.maxVisibleLines = n
	e.Invalidate()
}

// MaxVisibleLines returns the current visible-line cap (mostly for tests).
func (e *Editor) MaxVisibleLines() int { return e.maxVisibleLines }

// PaddingX returns the editor's horizontal content padding.
func (e *Editor) PaddingX() int { return e.paddingX }

// SetPaddingX changes the editor's horizontal content padding.
func (e *Editor) SetPaddingX(padding int) {
	padding = max(0, padding)
	if e.paddingX == padding {
		return
	}
	e.paddingX = padding
	e.Invalidate()
}

// AutocompleteMaxVisible returns the maximum number of autocomplete rows.
func (e *Editor) AutocompleteMaxVisible() int {
	if e.autocompleteMax == 0 {
		return 5
	}
	return e.autocompleteMax
}

// SetAutocompleteMaxVisible changes the autocomplete row limit.
func (e *Editor) SetAutocompleteMaxVisible(maxVisible int) {
	maxVisible = max(3, min(20, maxVisible))
	if e.AutocompleteMaxVisible() == maxVisible {
		return
	}
	e.autocompleteMax = maxVisible
	e.refreshAutocomplete()
	e.Invalidate()
}

// Text returns the editor content as a single string.
func (e *Editor) Text() string { return strings.Join(e.lines, "\n") }

// SetText replaces the editor content.
func (e *Editor) SetText(text string) {
	e.inputHistIdx = -1
	e.inputHistSaved = nil
	e.lines = strings.Split(text, "\n")
	e.cursor = [2]int{len(e.lines) - 1, len(e.lines[len(e.lines)-1])}
	e.refreshAutocomplete()
	e.Invalidate()
	if e.OnChange != nil {
		e.OnChange(e.Text())
	}
}

// Clear resets the editor.
func (e *Editor) Clear() {
	e.saveHistory()
	e.lines = []string{""}
	e.cursor = [2]int{0, 0}
	e.jumpMode = ""
	e.preferredVisualCol = nil
	e.snappedFromCursorCol = nil
	e.AutocompleteCancel()
	e.inputHistIdx = -1 // exit history browsing mode on clear
	e.inputHistSaved = nil
	e.pastes = nil
	e.pasteCounter = 0
	e.scrollOffset = 0
	e.Invalidate()
	if e.OnChange != nil {
		e.OnChange("")
	}
}

// GetExpandedText returns the editor content with any compact paste
// markers (`[paste #N +K lines]` / `[paste #N M chars]`) expanded back
// to their stored content.
func (e *Editor) GetExpandedText() string {
	return e.expandPasteMarkers(e.Text())
}

// expandPasteMarkers replaces every "[paste #N ...]" marker in text
// with the content stored under id N.
func (e *Editor) expandPasteMarkers(text string) string {
	if len(e.pastes) == 0 {
		return text
	}
	result := text
	for id, content := range e.pastes {
		re := pasteMarkerRegexp(id)
		result = re.ReplaceAllLiteralString(result, content)
	}
	return result
}

// ClearPastes drops the compact-paste store. Callers that consume the
// editor's text via GetExpandedText (e.g. submit) should call this
// after expanding so subsequent edits don't re-expand stale markers.
func (e *Editor) ClearPastes() {
	e.pastes = nil
	e.pasteCounter = 0
}

// AddToHistory adds a submitted text to the input history for Up/Down
// navigation. Skips empty strings and consecutive duplicates. Capped at 100 entries.
func (e *Editor) AddToHistory(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	if len(e.inputHistory) > 0 && e.inputHistory[0] == trimmed {
		return // no consecutive duplicates
	}
	e.inputHistory = append([]string{trimmed}, e.inputHistory...)
	if len(e.inputHistory) > 100 {
		e.inputHistory = e.inputHistory[:100]
	}
}

// navigateHistory moves through input history.
// direction: -1 = older (Up), +1 = newer (Down).
func (e *Editor) navigateHistory(direction int) {
	e.lastAction = ""
	if len(e.inputHistory) == 0 {
		return
	}
	newIdx := e.inputHistIdx - direction // Up(-1) increases idx, Down(+1) decreases
	if newIdx < -1 || newIdx >= len(e.inputHistory) {
		return
	}
	entering := e.inputHistIdx == -1 && newIdx >= 0
	if entering {
		// Capture current editor state before entering browse mode.
		e.inputHistSaved = &editorHistoryDraft{lines: append([]string(nil), e.lines...), cursor: e.cursor}
	}
	e.inputHistIdx = newIdx
	if e.inputHistIdx == -1 {
		if e.inputHistSaved == nil {
			e.setTextNoHistReset("", false)
			e.notifyChange()
			return
		}
		e.lines = append([]string(nil), e.inputHistSaved.lines...)
		e.cursor = e.inputHistSaved.cursor
		e.inputHistSaved = nil
		e.preferredVisualCol = nil
		e.snappedFromCursorCol = nil
		e.scrollOffset = 0
		e.Invalidate()
		e.notifyChange()
		return
	}
	e.setTextNoHistReset(e.inputHistory[e.inputHistIdx], direction == -1)
	if entering {
		// Entering browse mode pushes an undo snapshot of the draft. This
		// undo stack records post-edit states, so recording the
		// first browsed entry makes one undo return to the draft, however
		// far the browse went.
		e.saveHistory()
	}
	e.notifyChange()
}

// notifyChange reports the current text to OnChange after a text set or
// draft restore.
func (e *Editor) notifyChange() {
	if e.OnChange != nil {
		e.OnChange(e.Text())
	}
}

// setTextNoHistReset sets text without touching inputHistIdx: used by
// navigateHistory so undo-history undo doesn't interfere with browse state.
func (e *Editor) setTextNoHistReset(text string, cursorAtStart bool) {
	e.lines = strings.Split(text, "\n")
	if len(e.lines) == 0 {
		e.lines = []string{""}
	}
	if cursorAtStart {
		e.cursor = [2]int{0, 0}
	} else {
		e.cursor = [2]int{len(e.lines) - 1, len(e.lines[len(e.lines)-1])}
	}
	e.scrollOffset = 0
	e.Invalidate()
}

func (e *Editor) setCursorCol(col int) {
	e.cursor[1] = col
	e.preferredVisualCol = nil
	e.snappedFromCursorCol = nil
}

func isPasteMarker(segment string) bool {
	return len(segment) >= 10 && pasteMarkerSingle.MatchString(segment)
}

func (e *Editor) validPasteIDs() map[int]bool {
	ids := make(map[int]bool, len(e.pastes))
	for id := range e.pastes {
		ids[id] = true
	}
	return ids
}

func (e *Editor) segmentLine(text string) []graphemeSegment {
	validIDs := e.validPasteIDs()
	if len(validIDs) == 0 || !strings.Contains(text, "[paste #") {
		return graphemeSegments(text)
	}

	type markerSpan struct{ start, end int }
	var markers []markerSpan
	for _, m := range pasteMarkerRegex.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 4 {
			continue
		}
		idText := text[m[2]:m[3]]
		id := 0
		for _, r := range idText {
			id = id*10 + int(r-'0')
		}
		if validIDs[id] {
			markers = append(markers, markerSpan{start: m[0], end: m[1]})
		}
	}
	if len(markers) == 0 {
		return graphemeSegments(text)
	}

	base := graphemeSegments(text)
	result := make([]graphemeSegment, 0, len(base))
	markerIdx := 0
	for _, seg := range base {
		for markerIdx < len(markers) && markers[markerIdx].end <= seg.Start {
			markerIdx++
		}
		var marker *markerSpan
		if markerIdx < len(markers) {
			marker = &markers[markerIdx]
		}
		if marker != nil && seg.Start >= marker.start && seg.Start < marker.end {
			if seg.Start == marker.start {
				markerText := text[marker.start:marker.end]
				result = append(result, graphemeSegment{Text: markerText, Start: marker.start, End: marker.end, Width: widthx.VisibleWidth(markerText)})
			}
			continue
		}
		result = append(result, seg)
	}
	return result
}

func firstGrapheme(s string) string {
	g, _ := widthx.FirstGrapheme(s)
	return g
}

func wordWrapLine(line string, maxWidth int, preSegmented []graphemeSegment) []textChunk {
	if line == "" || maxWidth <= 0 {
		return []textChunk{{text: "", startIndex: 0, endIndex: 0}}
	}
	if widthx.VisibleWidth(line) <= maxWidth {
		return []textChunk{{text: line, startIndex: 0, endIndex: len(line)}}
	}

	segments := preSegmented
	if segments == nil {
		segments = graphemeSegments(line)
	}
	chunks := make([]textChunk, 0, len(segments))
	currentWidth := 0
	chunkStart := 0
	wrapOppIndex := -1
	wrapOppWidth := 0

	for i, seg := range segments {
		gWidth := seg.Width
		charIndex := seg.Start
		isWs := !isPasteMarker(seg.Text) && isWhitespaceGrapheme(seg.Text)

		if currentWidth+gWidth > maxWidth {
			if wrapOppIndex >= 0 && currentWidth-wrapOppWidth+gWidth <= maxWidth {
				chunks = append(chunks, textChunk{text: line[chunkStart:wrapOppIndex], startIndex: chunkStart, endIndex: wrapOppIndex})
				chunkStart = wrapOppIndex
				currentWidth -= wrapOppWidth
			} else if chunkStart < charIndex {
				chunks = append(chunks, textChunk{text: line[chunkStart:charIndex], startIndex: chunkStart, endIndex: charIndex})
				chunkStart = charIndex
				currentWidth = 0
			}
			wrapOppIndex = -1
		}

		// A segment wider than the line splits into its graphemes; a single
		// grapheme that is itself too wide (a CJK char at width 1) stands alone.
		if gWidth > maxWidth && len(seg.Text) > len(firstGrapheme(seg.Text)) {
			subChunks := wordWrapLine(seg.Text, maxWidth, nil)
			for j := range len(subChunks) - 1 {
				sc := subChunks[j]
				chunks = append(chunks, textChunk{text: sc.text, startIndex: charIndex + sc.startIndex, endIndex: charIndex + sc.endIndex})
			}
			last := subChunks[len(subChunks)-1]
			chunkStart = charIndex + last.startIndex
			currentWidth = widthx.VisibleWidth(last.text)
			wrapOppIndex = -1
			continue
		}

		currentWidth += gWidth
		if i+1 < len(segments) {
			next := segments[i+1]
			if isWs && (isPasteMarker(next.Text) || !isWhitespaceGrapheme(next.Text)) {
				wrapOppIndex = next.Start
				wrapOppWidth = currentWidth
			}
		}
	}

	chunks = append(chunks, textChunk{text: line[chunkStart:], startIndex: chunkStart, endIndex: len(line)})
	return chunks
}

// isEditorEmpty returns true when the editor contains only a single empty line.
func (e *Editor) isEditorEmpty() bool {
	return len(e.lines) == 1 && e.lines[0] == ""
}

func (e *Editor) saveHistory() {
	state := editorState{
		lines:        make([]string, len(e.lines)),
		cursor:       e.cursor,
		pastes:       clonePastes(e.pastes),
		pasteCounter: e.pasteCounter,
	}
	copy(state.lines, e.lines)
	if e.histIdx < len(e.history) {
		e.history = e.history[:e.histIdx]
	}
	e.history = append(e.history, state)
	e.histIdx = len(e.history)
}

// clonePastes returns a shallow copy of a paste registry so an undo snapshot
// is not mutated by later edits. Returns nil for an empty/nil registry.
func clonePastes(m map[int]string) map[int]string {
	if len(m) == 0 {
		return nil
	}
	c := make(map[int]string, len(m))
	maps.Copy(c, m)
	return c
}

// thinkingBorderSGR maps a thinking level string to a truecolor foreground
// SGR prefix for the editor's top divider. Colors are the dark theme's
// thinkingLow/Medium/High values, used as fg on the border rule (not
// bg-tints: no rendering-model adjustment needed).
//
//	thinkingLow    #5f87af
//	thinkingMedium #81a2be
//	thinkingHigh   #b294bb
//
// Returns "" for "off" so the caller falls back to borderMuted.
func thinkingBorderSGR(level string) string {
	switch level {
	case "low":
		return ThemeHexFg("#5f87af")
	case "medium":
		return ThemeHexFg("#81a2be")
	case "high":
		return ThemeHexFg("#b294bb")
	}
	return ""
}

// wrappedChunks returns the word-wrapped textChunk list for every logical line
// at the given width, memoizing per line. A line whose content is unchanged
// from the previous call at the same width reuses its cached chunks instead of
// re-running wordWrapLine (grapheme segmentation + wrapping). Output is
// identical to calling wordWrapLine(line, width, e.segmentLine(line)) for each
// line; the reuse is invisible. The comparison is by content, so SetText
// rebuilding the buffer still reuses chunks for lines whose text is unchanged.
func (e *Editor) wrappedChunks(width int) [][]textChunk {
	reuse := e.wrapCacheWidth == width
	out := make([][]textChunk, len(e.lines))
	for i, line := range e.lines {
		if reuse && i < len(e.wrapCacheLines) && e.wrapCacheLines[i] == line {
			out[i] = e.wrapCacheChunks[i]
			continue
		}
		out[i] = wordWrapLine(line, width, e.segmentLine(line))
	}
	snap := make([]string, len(e.lines))
	copy(snap, e.lines)
	e.wrapCacheWidth = width
	e.wrapCacheLines = snap
	e.wrapCacheChunks = out
	return out
}

// layoutText walks logical lines through the word-wrap cache and applies the
// cursor decoration to the appropriate visual chunk, returning the flat list
// of visual lines that Render emits between the top and bottom borders, before
// scroll-windowing.
func (e *Editor) layoutText(contentWidth int) []layoutLine {
	if len(e.lines) == 0 || (len(e.lines) == 1 && e.lines[0] == "") {
		return []layoutLine{{text: "", hasCursor: true, cursorPos: 0}}
	}

	// wordWrapLine returns a single chunk (startIndex 0) for a line that fits,
	// so the chunk loop below reproduces the former short-line fast path
	// exactly while letting wrappedChunks memoize the expensive wrapping.
	layoutLines := make([]layoutLine, 0, len(e.lines))
	for i, chunks := range e.wrappedChunks(contentWidth) {
		isCurrentLine := i == e.cursor[0]
		for ci, chunk := range chunks {
			cursorPos := e.cursor[1]
			isLastChunk := ci == len(chunks)-1
			hasCursorInChunk := false
			adjustedCursorPos := 0
			if isCurrentLine {
				if isLastChunk {
					hasCursorInChunk = cursorPos >= chunk.startIndex
					adjustedCursorPos = cursorPos - chunk.startIndex
				} else {
					hasCursorInChunk = cursorPos >= chunk.startIndex && cursorPos < chunk.endIndex
					if hasCursorInChunk {
						adjustedCursorPos = min(cursorPos-chunk.startIndex, len(chunk.text))
					}
				}
			}
			layoutLines = append(layoutLines, layoutLine{text: chunk.text, hasCursor: hasCursorInChunk, cursorPos: adjustedCursorPos})
		}
	}

	return layoutLines
}

func (e *Editor) buildVisualLines(width int) []string {
	var out []string
	emitCursorMarker := e.Focused && len(e.autocompleteItems) == 0
	for _, line := range e.layoutText(width) {
		if !line.hasCursor {
			out = append(out, line.text)
			continue
		}
		if line.cursorPos == len(line.text) && widthx.VisibleWidth(line.text) >= width && len(line.text) > 0 {
			segments := graphemeSegments(line.text)
			last := segments[len(segments)-1]
			marker := ""
			if emitCursorMarker {
				marker = widthx.CursorMarker
			}
			out = append(out, line.text[:last.Start]+marker+"\033[7m"+last.Text+"\033[0m")
			continue
		}
		out = append(out, renderCursorAt(line.text, line.cursorPos, emitCursorMarker, e.cursorStyle()))
	}
	return out
}

func (e *Editor) buildVisualLineMap(width int) []editorVisualLine {
	width = max(width, 1)
	if len(e.lines) == 0 {
		return []editorVisualLine{{logicalLine: 0, startCol: 0, length: 0}}
	}
	var out []editorVisualLine
	for i, chunks := range e.wrappedChunks(width) {
		for _, chunk := range chunks {
			out = append(out, editorVisualLine{logicalLine: i, startCol: chunk.startIndex, length: len(chunk.text)})
		}
	}
	if len(out) == 0 {
		return []editorVisualLine{{logicalLine: 0, startCol: 0, length: 0}}
	}
	return out
}

func (e *Editor) findVisualLineAt(visual []editorVisualLine, line, col int) int {
	for i, vl := range visual {
		if vl.logicalLine != line {
			continue
		}
		offset := col - vl.startCol
		isLastSegment := i == len(visual)-1 || visual[i+1].logicalLine != vl.logicalLine
		if offset >= 0 && (offset < vl.length || (isLastSegment && offset == vl.length)) {
			return i
		}
	}
	return max(0, len(visual)-1)
}

func (e *Editor) findCurrentVisualLine(visual []editorVisualLine) int {
	return e.findVisualLineAt(visual, e.cursor[0], e.cursor[1])
}

func (e *Editor) isOnFirstVisualLine() bool {
	visual := e.buildVisualLineMap(e.renderWidth)
	return e.findCurrentVisualLine(visual) == 0
}

func (e *Editor) isOnLastVisualLine() bool {
	visual := e.buildVisualLineMap(e.renderWidth)
	return e.findCurrentVisualLine(visual) == len(visual)-1
}

// findCursorVisualLine returns the index in visual[] that corresponds to
// the cursor's logical position. Returns 0 when no chunk is decorated
// with the cursor.
func (e *Editor) findCursorVisualLine(visual []string) int {
	mapping := e.buildVisualLineMap(e.renderWidth)
	idx := e.findCurrentVisualLine(mapping)
	if idx >= len(visual) {
		return max(0, len(visual)-1)
	}
	return idx
}

// borderSGR returns the active border-color SGR (or "" + reset) used by
// scrollIndicatorBorder so the indicator inherits bash / thinking-level /
// extension coloring without leaking attributes past the end of the row.
func (e *Editor) borderSGR() string {
	if e.IsBashMode() {
		return bashHeaderColor()
	}
	if sgr := thinkingBorderSGR(e.ThinkingLevel); sgr != "" {
		return sgr
	}
	return ActiveTheme().BorderMuted
}

// scrollIndicatorBorder builds a single border row reading
// "─── arrow N more " followed by enough horizontal rule to fill width.
func scrollIndicatorBorder(arrow string, n, width int, plainBorder, sgr string) string {
	indicator := fmt.Sprintf("─── %s %d more ", arrow, n)
	indicatorWidth := widthx.VisibleWidth(indicator)
	if indicatorWidth >= width {
		// Indicator would overflow; fall back to the plain border to
		// avoid soft-wrap; for the width-too-small edge case the plain
		// border is acceptable.
		return plainBorder
	}
	remaining := strings.Repeat("\u2500", width-indicatorWidth)
	reset := "\033[0m"
	return sgr + indicator + remaining + reset
}

func (e *Editor) Render(width int) []string {
	width = max(width, 1)
	if e.panel != nil {
		return e.renderPanel(width)
	}
	// Top + bottom dashed dividers around the editor content. The editor
	// owns these rows because they are part of its visual rather than
	// layout-level spacing.
	border := strings.Repeat("\u2500", width)
	// Bash-mode border (when buffer starts with `!`): the border switches
	// between borderMuted and `bashMode` (orange) based on the leading
	// character. Color matches the
	// `bashHeaderColor` used by `BashExecutionBlock` so the visual
	// link from typing to executed block is obvious.
	// Thinking level border color overrides borderMuted when not
	// in bash mode. Maps "low"/"medium"/"high" to per-level fg colors
	// Bash mode takes precedence.
	dimBorder := ActiveTheme().BorderMuted + border + SGRFgReset
	if e.IsBashMode() {
		dimBorder = bashHeaderColor() + border + "\033[0m"
	} else if sgr := thinkingBorderSGR(e.ThinkingLevel); sgr != "" {
		dimBorder = sgr + border + "\033[0m"
	}
	topBorder := dimBorder
	// One blank row above the top border so the editor frame doesn't
	// sit flush against the last chat line.
	// Build the full visual layout first (all chunks of all logical
	// lines, with cursor decoration applied), then scroll/window it
	// by scrollOffset + maxVisibleLines.
	paddingX := min(e.paddingX, max(0, (width-1)/2))
	contentWidth := max(1, width-paddingX*2)
	layoutWidth := contentWidth
	if paddingX == 0 {
		// Reserve the rightmost terminal column for
		// the cursor when the editor has no horizontal padding.
		layoutWidth = max(1, contentWidth-1)
	}
	visible, visual, end := e.visibleContent(layoutWidth)
	if paddingX > 0 {
		padding := strings.Repeat(" ", paddingX)
		for i, line := range visible {
			right := strings.Repeat(" ", max(0, contentWidth-widthx.VisibleWidth(line)))
			visible[i] = padding + line + right + padding
		}
	}

	// Render top border (with "↑ N more" indicator if scrolled down).
	top := topBorder
	if e.scrollOffset > 0 {
		top = scrollIndicatorBorder("↑", e.scrollOffset, width, dimBorder, e.borderSGR())
	}
	top = e.renderStatusBorder(width, e.scrollOffset, top)
	e.renderedContentTop = 2 // One spacing row and the top border precede content.
	e.renderedContentX = paddingX
	e.renderedAutocompleteTop = 2 + len(visible) + 1
	out := []string{"", top}
	out = append(out, visible...)

	// Bottom border (with "↓ N more" indicator if more content below).
	bot := dimBorder
	linesBelow := len(visual) - end
	if linesBelow > 0 {
		bot = scrollIndicatorBorder("↓", linesBelow, width, dimBorder, e.borderSGR())
	}
	out = append(out, bot)

	// Render slash-autocomplete popup as additional rows
	// below the editor frame.
	e.renderedAutocompleteHeight = 0
	if len(e.autocompleteItems) > 0 {
		autocomplete := e.renderAutocomplete(contentWidth)
		e.renderedAutocompleteHeight = len(autocomplete)
		if paddingX > 0 {
			padding := strings.Repeat(" ", paddingX)
			for i, line := range autocomplete {
				right := strings.Repeat(" ", max(0, contentWidth-widthx.VisibleWidth(line)))
				autocomplete[i] = padding + line + right + padding
			}
		}
		out = append(out, autocomplete...)
	}
	return out
}

// visibleContent lays out the buffer at layoutWidth, keeps the cursor row in
// view, and returns the rows to draw, every visual row, and the index one past
// the last drawn row.
func (e *Editor) visibleContent(layoutWidth int) (visible, visual []string, end int) {
	e.renderWidth = layoutWidth
	visual = e.buildVisualLines(layoutWidth)
	cursorVisualIdx := e.findCursorVisualLine(visual)

	maxVis := e.maxVisibleLines
	if maxVis < 1 {
		maxVis = 5
	}
	if cursorVisualIdx >= 0 {
		if cursorVisualIdx < e.scrollOffset {
			e.scrollOffset = cursorVisualIdx
		} else if cursorVisualIdx >= e.scrollOffset+maxVis {
			e.scrollOffset = cursorVisualIdx - maxVis + 1
		}
	}
	maxOffset := max(0, len(visual)-maxVis)
	e.scrollOffset = max(0, min(e.scrollOffset, maxOffset))

	end = min(e.scrollOffset+maxVis, len(visual))
	visible = visual[e.scrollOffset:end]
	e.renderedVisibleLineCount = len(visible)
	return visible, visual, end
}

// HandleMouse activates autocomplete rows and positions the editor cursor on a
// synthesized left click. Press, drag, and release remain unhandled so the
// alternate-screen renderer can own text selection. Autocomplete clicks retain the pressed item across scrolling.
func (e *Editor) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	autocompleteStartRow := e.renderedAutocompleteTop
	if len(e.autocompleteItems) > 0 && event.Y >= autocompleteStartRow && event.Y < autocompleteStartRow+e.renderedAutocompleteHeight {
		if event.Type == MouseWheel {
			if event.WheelDelta == 0 {
				return nil
			}
			previous := e.autocompleteCursor
			delta := 1
			if event.WheelDelta < 0 {
				delta = -1
			}
			e.AutocompleteMove(delta)
			changed := e.autocompleteCursor != previous
			return &TuiMouseDispatchResult{Handled: true, Focus: true, Render: new(changed)}
		}
		start, end := e.autocompleteVisibleRange()
		itemIndex := start + event.Y - autocompleteStartRow
		if itemIndex < start || itemIndex >= end {
			return nil
		}
		switch event.Type {
		case MousePress:
			if event.Button != MouseButtonLeft {
				return nil
			}
			e.autocompleteMousePressedIndex = new(itemIndex)
			e.autocompleteCursor = itemIndex
			e.Invalidate()
			return &TuiMouseDispatchResult{Handled: true, Focus: true}
		case MouseClick:
			if event.Button != MouseButtonLeft {
				return nil
			}
			if e.autocompleteMousePressedIndex != nil {
				itemIndex = *e.autocompleteMousePressedIndex
			}
			e.autocompleteMousePressedIndex = nil
			e.autocompleteCursor = itemIndex
			e.AutocompleteAccept()
			e.Invalidate()
			return &TuiMouseDispatchResult{Handled: true, Focus: true}
		default:
			return nil
		}
	}

	if event.Type != MouseClick || event.Button != MouseButtonLeft {
		return nil
	}
	contentStartRow := e.renderedContentTop
	if event.Y < contentStartRow || event.Y >= contentStartRow+e.renderedVisibleLineCount {
		return &TuiMouseDispatchResult{Handled: true, Focus: true}
	}

	visualLines := e.buildVisualLineMap(e.renderWidth)
	visualLineIndex := e.scrollOffset + event.Y - contentStartRow
	if visualLineIndex < 0 || visualLineIndex >= len(visualLines) {
		return &TuiMouseDispatchResult{Handled: true, Focus: true}
	}
	visualLine := visualLines[visualLineIndex]
	logicalLine := e.lines[visualLine.logicalLine]
	chunkEnd := min(len(logicalLine), visualLine.startCol+visualLine.length)
	chunk := logicalLine[visualLine.startCol:chunkEnd]
	targetColumn := max(0, event.X-e.renderedContentX)
	visibleColumn := 0
	targetIndex := len(chunk)
	lastGraphemeIndex := 0
	for _, grapheme := range graphemeSegments(chunk) {
		nextColumn := visibleColumn + grapheme.Width
		lastGraphemeIndex = grapheme.Start
		if targetColumn < nextColumn {
			targetIndex = grapheme.Start
			break
		}
		visibleColumn = nextColumn
	}
	isLastSegment := visualLineIndex == len(visualLines)-1 || visualLines[visualLineIndex+1].logicalLine != visualLine.logicalLine
	if !isLastSegment && targetIndex == len(chunk) && len(chunk) > 0 {
		targetIndex = lastGraphemeIndex
	}

	e.cursor[0] = visualLine.logicalLine
	e.setCursorCol(visualLine.startCol + targetIndex)
	e.lastAction = ""
	e.inputHistIdx = -1
	e.inputHistSaved = nil
	if len(e.autocompleteItems) > 0 {
		e.refreshAutocomplete()
	}
	e.Invalidate()
	return &TuiMouseDispatchResult{Handled: true, Focus: true}
}

func (e *Editor) autocompleteVisibleRange() (start, end int) {
	n := len(e.autocompleteItems)
	maxVisible := e.autocompleteMax
	if maxVisible <= 0 {
		maxVisible = 5
	}
	if n > maxVisible {
		if e.autocompleteCursor >= maxVisible {
			start = e.autocompleteCursor - maxVisible + 1
		}
		if start+maxVisible > n {
			start = n - maxVisible
		}
	}
	end = min(start+maxVisible, n)
	return start, end
}

// IsBashMode reports whether the buffer is in bash-prefix mode
// (first non-whitespace char is `!`). Used to switch the
// editor border color and to suppress slash-autocomplete in this state.
func (e *Editor) IsBashMode() bool {
	if len(e.lines) == 0 {
		return false
	}
	trimmed := strings.TrimLeft(e.lines[0], " \t")
	return strings.HasPrefix(trimmed, "!")
}

// renderAutocomplete produces the popup rows in select-list styling: the
// selected row uses an accent-colored `→ ` prefix + row text,
// while unselected descriptions render in muted color.
func (e *Editor) renderAutocomplete(width int) []string {
	n := len(e.autocompleteItems)
	if n == 0 {
		return nil
	}
	maxVisible := e.autocompleteMax
	if maxVisible <= 0 {
		maxVisible = 5
	}
	start := 0
	if n > maxVisible {
		if e.autocompleteCursor >= maxVisible {
			start = e.autocompleteCursor - maxVisible + 1
		}
		if start+maxVisible > n {
			start = n - maxVisible
		}
	}
	end := min(start+maxVisible, n)

	const (
		selectedPrefix = "→ "
		noCursor       = "  "
		prefixWidth    = 2
		primaryGap     = 2
		minDescWidth   = 10
		widthThreshold = 40
	)
	th := ActiveTheme()
	accent := cmp.Or(th.Accent, "\x1b[38;2;138;190;183m")
	muted := cmp.Or(th.Muted, "\x1b[38;2;128;128;128m")

	primaryColumnWidth := 0
	for _, it := range e.autocompleteItems {
		if l := widthx.VisibleWidth(it.Label) + primaryGap; l > primaryColumnWidth {
			primaryColumnWidth = l
		}
	}
	primaryColumnWidth = min(max(primaryColumnWidth, 12), 32)
	effLabelW := max(min(primaryColumnWidth, width-prefixWidth-4), 1)
	descColStart := prefixWidth + effLabelW + primaryGap
	descBudget := width - descColStart - 1
	// Only slash-command autocomplete uses the 2-column description layout.
	// File/path completion uses the default select-list layout,
	// which renders labels only even when items carry descriptions.
	showDesc := strings.HasPrefix(e.autocompletePrefix, "/") && width > widthThreshold && descBudget >= minDescWidth

	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		it := e.autocompleteItems[i]
		isCursor := i == e.autocompleteCursor
		prefix := noCursor
		if isCursor {
			prefix = selectedPrefix
		}

		if !showDesc {
			label := widthx.TruncateToWidth(it.Label, max(width-prefixWidth-2, 1), "", false)
			row := prefix + label
			if isCursor {
				lines = append(lines, padOrTrunc(accent+row+"\x1b[39m", width))
			} else {
				lines = append(lines, padOrTrunc(row, width))
			}
			continue
		}

		maxPrimaryWidth := max(1, effLabelW-primaryGap)
		label := cmp.Or(widthx.TruncateToWidth(it.Label, maxPrimaryWidth, "", false), it.Label)
		labelPad := max(effLabelW-widthx.VisibleWidth(label), 1)
		desc := widthx.TruncateToWidth(normalizeToSingleLine(it.Description), descBudget, "", false)
		spacing := strings.Repeat(" ", labelPad)
		if isCursor {
			row := prefix + label + spacing + desc
			lines = append(lines, padOrTrunc(accent+row+"\x1b[39m", width))
			continue
		}
		row := prefix + label + muted + spacing + desc + "\x1b[39m"
		lines = append(lines, padOrTrunc(row, width))
	}
	// Scroll-position counter. Only emitted when the popup is
	// scrolled (some items off-screen): `  (idx+1/total)` in dim.
	// Hidden when everything fits in `max` so unfiltered short lists
	// (e.g. `/he` filtered to 3 items, max=5) don't get a noisy line.
	if start > 0 || end < n {
		// Truncated to width - 2, so empty when width - 2 <= 0.
		counter := widthx.TruncateToWidth(fmt.Sprintf("  (%d/%d)", e.autocompleteCursor+1, n), width-2, "", false)
		muted := cmp.Or(ActiveTheme().Muted, "\x1b[38;2;128;128;128m")
		lines = append(lines, muted+counter+"\x1b[39m")
	}
	return lines
}

// AsyncFileSearcher is an optional capability of the local autocomplete
// provider: it returns a deferred fd-backed @-file search that the editor
// runs off the input thread with cancellation, so a deep directory walk
// cannot block keystrokes. When ok is false the editor falls back to the
// synchronous GetSuggestions result.
type AsyncFileSearcher interface {
	FileSearchTask(lines []string, cursorLine, cursorCol int) (prefix string, run func(context.Context) []AutocompleteItem, ok bool)
}

// SetAsyncApply installs a scheduler that runs the given closure on the
// host's main loop (single-threaded with keystroke handling). The editor
// uses it to apply async suggestion results without mutating its
// lock-free state from a worker goroutine. When unset, results are
// applied inline on the worker (single-threaded callers / tests only).
func (e *Editor) SetAsyncApply(schedule func(func())) { e.scheduleAsyncApply = schedule }

// SetAutocomplete attaches an AutocompleteProvider. Pass nil to detach.
func (e *Editor) SetAutocomplete(p AutocompleteProvider) {
	e.autocomplete = p
	if e.autocompleteMax == 0 {
		e.autocompleteMax = 5
	}
	e.refreshAutocomplete()
}

// AutocompleteOpen reports whether the popup is currently visible.
// Used by the host (interactive.go) to gate Esc/Enter handling.
func (e *Editor) AutocompleteOpen() bool { return len(e.autocompleteItems) > 0 }

// closeAutocompletePopup clears the popup's items, cursor and prefix.
func (e *Editor) closeAutocompletePopup() {
	e.autocompleteItems = nil
	e.autocompleteCursor = 0
	e.autocompletePrefix = ""
	e.Invalidate()
}

// AutocompleteCancel dismisses the popup without applying.
func (e *Editor) AutocompleteCancel() {
	e.autocompleteMousePressedIndex = nil
	e.autocompleteRequestID++
	e.asyncSeq++
	if e.asyncCancel != nil {
		e.asyncCancel()
		e.asyncCancel = nil
	}
	if len(e.autocompleteItems) == 0 {
		return
	}
	e.closeAutocompletePopup()
}

// AutocompleteAccept applies the currently-selected suggestion.
// Returns submit=true iff the host should now submit the editor
// (Enter on a slash-name prefix inserts and falls through to submit).
func (e *Editor) AutocompleteAccept() (submit bool) {
	e.autocompleteMousePressedIndex = nil
	if len(e.autocompleteItems) == 0 || e.autocomplete == nil {
		return false
	}
	// The cached popup (autocompleteItems/autocompletePrefix) is filled by a
	// SetAsyncApply callback posted from refreshAutocomplete so a popup
	// repaint can't grow the viewport mid-keystroke (see the comment there),
	// which defers the actual update onto the host's main loop instead of
	// setting it synchronously with the keystroke that triggered it. A fast
	// typed sequence (e.g. a whole slash command name delivered in one
	// burst) can race ahead of that deferred apply and leave the cache
	// pinned to an earlier, shorter prefix than what the buffer actually
	// holds.
	//
	// The local provider's own answer is always available synchronously
	// (GetSuggestions never itself defers), so it is re-resolved against the
	// buffer as it stands right now before accepting -- this recovers what a
	// synchronous lookup at the time of Enter would have produced, including
	// completing a still-partial prefix to its best match.
	res := e.autocomplete.GetSuggestions(e.lines, e.cursor[0], e.cursor[1])
	if res == nil || len(res.Items) == 0 {
		e.closeAutocompletePopup()
		return false
	}
	e.autocompleteItems = res.Items
	e.autocompletePrefix = res.Prefix
	e.autocompleteQueryCursor = e.cursor
	if e.autocompleteCursor >= len(res.Items) {
		e.autocompleteCursor = 0
	}
	item := e.autocompleteItems[e.autocompleteCursor]
	prefix := e.autocompletePrefix
	newLines, nl, nc := e.autocomplete.ApplyCompletion(e.lines, e.cursor[0], e.cursor[1], item, prefix)
	e.lines = newLines
	e.cursor = [2]int{nl, nc}
	e.saveHistory()
	// Slash-name prefix → submit. Anything else (arg completion) → no submit.
	isSlashName := strings.HasPrefix(prefix, "/") && !strings.ContainsAny(prefix, " \t")
	e.closeAutocompletePopup()
	if !isSlashName && e.OnChange != nil {
		e.OnChange(e.Text())
	}
	return isSlashName
}

// AutocompleteMove shifts the popup cursor by delta (clamped). No-op
// when the popup is closed.
func (e *Editor) AutocompleteMove(delta int) {
	n := len(e.autocompleteItems)
	if n == 0 {
		return
	}
	c := max(e.autocompleteCursor+delta, 0)
	if c >= n {
		c = n - 1
	}
	if c != e.autocompleteCursor {
		e.autocompleteCursor = c
		e.Invalidate()
	}
}

// forceFileAutocomplete triggers a forced file-completion query on Tab
// when the popup is closed. Returns true when suggestions were produced
// and the popup is now open.
func (e *Editor) forceFileAutocomplete() bool {
	if e.autocomplete == nil || e.IsBashMode() {
		return false
	}
	// Don't force-complete inside a slash-command-name token: typing
	// `/he` + Tab should accept the current popup selection, which is
	// already handled by the open-popup branch above. When the popup is
	// closed for `/...`, the slash provider already returns nothing, so
	// we let the keystroke fall through to the regular Tab dispatch.
	line := e.lines[e.cursor[0]]
	col := min(e.cursor[1], len(line))
	before := line[:col]
	trimmed := strings.TrimLeft(before, " \t")
	if strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, " ") {
		return false
	}
	fp, ok := e.autocomplete.(ForcefulAutocompleteProvider)
	if !ok {
		return false
	}
	res := fp.GetSuggestionsForce(e.lines, e.cursor[0], e.cursor[1])
	if res == nil || len(res.Items) == 0 {
		return false
	}
	if len(res.Items) == 1 {
		item := res.Items[0]
		newLines, nl, nc := e.autocomplete.ApplyCompletion(e.lines, e.cursor[0], e.cursor[1], item, res.Prefix)
		e.lines = newLines
		e.cursor = [2]int{nl, nc}
		e.saveHistory()
		e.closeAutocompletePopup()
		if e.OnChange != nil {
			e.OnChange(e.Text())
		}
		return true
	}
	e.autocompleteItems = res.Items
	e.autocompletePrefix = res.Prefix
	e.autocompleteQueryCursor = e.cursor
	e.autocompleteCursor = 0
	e.Invalidate()
	return true
}

// refreshAutocomplete re-queries the provider after a mutation. Called
// from insert / backspace / word-delete / SetText paths. Closes the
// popup when the provider returns nil.
func (e *Editor) refreshAutocomplete() {
	e.autocompleteMousePressedIndex = nil
	e.autocompleteRequestID++
	// Always cancel any in-flight async query so its late delivery
	// can't overwrite items belonging to a newer buffer state.
	if e.asyncCancel != nil {
		e.asyncCancel()
		e.asyncCancel = nil
	}
	// Resolve any deferred fd-backed @-file search up front so a deep tree
	// walk runs off the input thread instead of blocking keystrokes.
	var fileTask func(context.Context) []AutocompleteItem
	var filePrefix string
	if afs, ok := e.autocomplete.(AsyncFileSearcher); ok && !e.IsBashMode() {
		if pfx, run, ok := afs.FileSearchTask(e.lines, e.cursor[0], e.cursor[1]); ok {
			fileTask, filePrefix = run, pfx
		}
	}
	if e.autocomplete == nil {
		return
	}
	// Never show the slash-autocomplete popup while the
	// editor is in bash mode (`!` prefix).
	if e.IsBashMode() {
		if len(e.autocompleteItems) > 0 {
			e.closeAutocompletePopup()
		}
		return
	}
	res := e.autocomplete.GetSuggestions(e.lines, e.cursor[0], e.cursor[1])
	if res == nil || len(res.Items) == 0 {
		// Only clear synchronously when no async fd search is pending;
		// otherwise keep the prior popup until fd returns to avoid flicker.
		if fileTask == nil && len(e.autocompleteItems) > 0 {
			e.closeAutocompletePopup()
		}
		e.runFileSearch(nil, fileTask, filePrefix)
		return
	}
	requestID, text, cursor := e.autocompleteRequestID, e.Text(), e.cursor
	apply := func() {
		if requestID != e.autocompleteRequestID || text != e.Text() || cursor != e.cursor {
			return
		}
		e.autocompleteMousePressedIndex = nil
		e.autocompleteItems = res.Items
		e.autocompletePrefix = res.Prefix
		e.autocompleteQueryCursor = cursor
		if e.autocompleteCursor >= len(res.Items) {
			e.autocompleteCursor = 0
		}
		e.Invalidate()
	}
	// Local suggestions are deferred too. Paint the input before a popup
	// can grow the viewport, and reject results superseded by submit/cancel.
	if e.scheduleAsyncApply != nil {
		e.scheduleAsyncApply(apply)
	} else {
		apply()
	}
	e.runFileSearch(res, fileTask, filePrefix)
}

// runFileSearch runs the deferred @-file search, if any, on a worker and
// shows its items after baseRes's (the local provider's result, or nil).
func (e *Editor) runFileSearch(baseRes *AutocompleteSuggestions, fileTask func(context.Context) []AutocompleteItem, filePrefix string) {
	if fileTask == nil {
		return
	}
	e.asyncSeq++
	mySeq := e.asyncSeq
	ctx, cancel := context.WithCancel(context.Background())
	e.asyncCancel = cancel
	// Snapshot buffer state so the result can be checked against it.
	linesCopy := slices.Clone(e.lines)
	cursorLine, cursorCol := e.cursor[0], e.cursor[1]
	go func() {
		// Natural attachment completion is debounced for 20 ms. Waiting
		// inside the owned worker lets a superseding keystroke cancel before fd
		// starts, without a timer goroutine mutating editor state.
		if strings.HasPrefix(filePrefix, "@") {
			timer := time.NewTimer(attachmentAutocompleteDebounce)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		}
		fileItems := fileTask(ctx)
		if ctx.Err() != nil {
			return // cancelled by the next mutation
		}
		// The result applies even when empty, which clears a stale popup.
		var final []AutocompleteItem
		finalPrefix := filePrefix
		if baseRes != nil {
			final = append(final, baseRes.Items...)
			finalPrefix = cmp.Or(baseRes.Prefix, filePrefix)
		}
		final = append(final, fileItems...)
		// Apply on the host's main loop, single-threaded with keystroke
		// handling. The seq re-check runs there too (asyncSeq is written only
		// on the main loop), discarding results superseded by a newer
		// keystroke.
		apply := func() {
			if ctx.Err() != nil || mySeq != e.asyncSeq || e.cursor != [2]int{cursorLine, cursorCol} || !slices.Equal(e.lines, linesCopy) {
				return
			}
			e.autocompleteMousePressedIndex = nil
			e.autocompleteItems = final
			e.autocompletePrefix = finalPrefix
			e.autocompleteQueryCursor = [2]int{cursorLine, cursorCol}
			if e.autocompleteCursor >= len(final) {
				e.autocompleteCursor = 0
			}
			e.Invalidate()
		}
		if e.scheduleAsyncApply != nil {
			e.scheduleAsyncApply(apply)
		} else {
			apply()
		}
	}()
}

// renderCursorAt splices an inverted-video cursor cell into `line` at the
// given byte offset. If the offset is at end-of-line a synthetic space
// is appended so the cursor still has somewhere to render.
func renderCursorAt(line string, col int, emitMarker bool, style func(cell string) string) string {
	marker := ""
	if emitMarker {
		marker = widthx.CursorMarker
	}
	if style == nil {
		style = reverseCursor
	}
	if col >= len(line) {
		return line + marker + style(" ")
	}
	end := nextGraphemeEnd(line, col)
	return line[:col] + marker + style(line[col:end]) + line[end:]
}

// reverseCursor draws the cursor cell in reverse video.
func reverseCursor(cell string) string { return "\033[7m" + cell + "\033[0m" }

// pasteMarkerRegexp returns the regexp matching the marker
// "[paste #<id>]" plus the optional " +K lines" / " M chars" suffix
// produced by handlePasteFlush.
func pasteMarkerRegexp(id int) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`\[paste #%d( (\+\d+ lines|\d+ chars))?\]`, id))
}

// handlePasteFlush inserts a buffered bracketed paste. Large pastes
// (>10 lines or >1000 chars) are replaced with a compact marker and
// stashed in e.pastes for later expansion via GetExpandedText.
func (e *Editor) handlePasteFlush(buf string) {
	if buf == "" {
		return
	}
	// Cancel autocomplete first and insert through a path that never
	// triggers it, so a paste never leaves a popup open that
	// would swallow the following Enter.
	e.AutocompleteCancel()
	// Some terminals re-encode control bytes inside bracketed paste as
	// CSI-u Ctrl+<letter> sequences (ESC [ <codepoint> ; 5 u). Decode
	// those back to their literal byte before normalization so Ctrl+J
	// becomes a real newline rather than leaking "[106;5u" text.
	decoded := pasteCtrlCSIU.ReplaceAllStringFunc(buf, func(seq string) string {
		match := pasteCtrlCSIU.FindStringSubmatch(seq)
		if len(match) != 2 {
			return seq
		}
		cp, err := strconv.Atoi(match[1])
		if err != nil {
			return seq
		}
		switch {
		case cp >= 97 && cp <= 122:
			return string(rune(cp - 96))
		case cp >= 65 && cp <= 90:
			return string(rune(cp - 64))
		default:
			return seq
		}
	})
	// Normalize line endings and tabs (\r\n / \r → \n, tab → 4 spaces).
	clean := strings.ReplaceAll(decoded, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	clean = strings.ReplaceAll(clean, "\t", "    ")
	// Filter non-printable except newline.
	var filtered strings.Builder
	filtered.Grow(len(clean))
	for _, r := range clean {
		if r == '\n' || r >= 32 {
			filtered.WriteRune(r)
		}
	}
	text := filtered.String()
	if text == "" {
		return
	}
	if strings.ContainsRune("/~.", rune(text[0])) && e.cursor[0] >= 0 && e.cursor[0] < len(e.lines) {
		line := e.lines[e.cursor[0]]
		cursor := min(max(e.cursor[1], 0), len(line))
		if cursor > 0 {
			before, _ := utf8.DecodeLastRuneInString(line[:cursor])
			if isJSWordChar(before) {
				text = " " + text
			}
		}
	}
	lineCount := strings.Count(text, "\n") + 1
	charCount := jsStringLength(text)
	if lineCount > 10 || charCount > 1000 {
		if e.pastes == nil {
			e.pastes = make(map[int]string)
		}
		e.pasteCounter++
		id := e.pasteCounter
		e.pastes[id] = text
		var marker string
		if lineCount > 10 {
			marker = fmt.Sprintf("[paste #%d +%d lines]", id, lineCount)
		} else {
			marker = fmt.Sprintf("[paste #%d %d chars]", id, charCount)
		}
		e.saveHistory()
		e.insert(marker)
		return
	}
	e.saveHistory()
	e.insert(text)
}

func jsStringLength(text string) int {
	length := 0
	for _, r := range text {
		length += utf16.RuneLen(r)
	}
	return length
}

func isJSWordChar(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

func (e *Editor) HandleInput(data string) {
	beforeText := e.Text()
	defer func() {
		if e.OnChange != nil && e.Text() != beforeText {
			e.OnChange(e.Text())
		}
	}()

	if e.jumpMode != "" {
		if kb := Keybindings(); kb.Matches(data, KBEditorJumpForward) || kb.Matches(data, KBEditorJumpBackward) {
			e.jumpMode = ""
			return
		}
		if len(data) > 0 && data[0] >= 0x20 {
			direction := e.jumpMode
			e.jumpMode = ""
			e.jumpToChar(data, direction)
			return
		}
		e.jumpMode = ""
	}

	// ─── Bracketed paste handling ────────────────────────────────────────
	// Content between \x1b[200~ and \x1b[201~ is treated as a single insert.
	if e.isInPaste {
		e.pasteBuffer += data
		if endIndex := strings.Index(e.pasteBuffer, "\x1b[201~"); endIndex >= 0 {
			pasteContent := e.pasteBuffer[:endIndex]
			remainder := e.pasteBuffer[endIndex+len("\x1b[201~"):]
			e.isInPaste = false
			e.pasteBuffer = ""
			if pasteContent != "" {
				e.handlePasteFlush(pasteContent)
			}
			if remainder != "" {
				e.HandleInput(remainder)
			}
		}
		return
	}
	if idx := strings.Index(data, "\x1b[200~"); idx >= 0 {
		// Start of paste: process anything before the marker normally.
		if idx > 0 {
			e.HandleInput(data[:idx])
		}
		e.isInPaste = true
		e.pasteBuffer = ""
		remainder := data[idx+6:] // len("\x1b[200~") == 6
		if remainder != "" {
			e.HandleInput(remainder)
		}
		return
	}

	// ─── Autocomplete navigation ─────────────────────────────────────────
	// When the autocomplete popup is open, ↑/↓/Ctrl+P/Ctrl+N
	// navigate the popup and Tab accepts the selection. Esc and Enter
	// are handled by the host (interactive.go) before reaching us.
	//
	// Routed through the TUI keybinding registry so user overrides in
	// ~/.wopr/keybindings.json (e.g. swapping select up/down) take effect
	// here too.
	kb := Keybindings()
	if len(e.autocompleteItems) > 0 {
		switch {
		case kb.Matches(data, KBSelectUp), data == "\x10": // up / Ctrl+P (legacy)
			e.AutocompleteMove(-1)
			return
		case kb.Matches(data, KBSelectDown), data == "\x0e": // down / Ctrl+N (legacy)
			e.AutocompleteMove(1)
			return
		case kb.Matches(data, KBInputTab): // Tab: accept (no submit; submit is reserved for Enter)
			e.AutocompleteAccept()
			e.refreshAutocomplete()
			return
		}
	} else if kb.Matches(data, KBInputTab) {
		// Popup closed: Tab triggers a forced file-completion request.
		// Only fires when the editor is not in a slash-command-name
		// context.
		if e.forceFileAutocomplete() {
			return
		}
	}

	// Dedicated history actions always browse entries instead of moving the
	// cursor. They are checked after the copy, undo, tab, deletion, and
	// kill-ring actions and before cursor movement, newline, and submit,
	// so a key bound to both keeps the earlier action.
	if !e.matchesActionBeforeHistory(kb, data) {
		if kb.Matches(data, KBEditorHistoryPrevious) {
			e.AutocompleteCancel()
			e.navigateHistory(-1)
			return
		}
		if kb.Matches(data, KBEditorHistoryNext) {
			e.AutocompleteCancel()
			e.navigateHistory(1)
			return
		}
	}

	// ─── Editor dispatch ─────────────────────────────────────────────────
	// All editing actions go through the keybinding registry so user
	// overrides take effect. Each branch corresponds to one keybinding ID;
	// the registry knows which raw terminal sequences map to each ID.
	switch {
	case kb.Matches(data, KBEditorCursorUp):
		switch {
		case e.isOnFirstVisualLine() && (e.isEditorEmpty() || e.inputHistIdx >= 0 || e.cursor[1] == 0):
			e.navigateHistory(-1)
		case e.isOnFirstVisualLine():
			e.lastAction = ""
			e.setCursorCol(0)
			e.Invalidate()
		default:
			e.moveCursor(-1, 0)
		}
	case kb.Matches(data, KBEditorCursorDown):
		switch {
		case e.inputHistIdx >= 0 && e.isOnLastVisualLine():
			e.navigateHistory(1)
		case e.isOnLastVisualLine():
			e.lastAction = ""
			e.setCursorCol(len(e.lines[e.cursor[0]]))
			e.Invalidate()
		default:
			e.moveCursor(1, 0)
		}
	case kb.Matches(data, KBEditorCursorWordRight):
		// alt+right / ctrl+right / alt+f: cursor word forward
		e.cursorWordForward()
	case kb.Matches(data, KBEditorCursorWordLeft):
		// alt+left / ctrl+left / alt+b: cursor word backward
		e.cursorWordBackward()
	case kb.Matches(data, KBEditorCursorRight):
		e.moveCursor(0, 1)
	case kb.Matches(data, KBEditorCursorLeft):
		e.moveCursor(0, -1)
	case matchesEditorNewLine(data, kb):
		if e.shouldSubmitOnBackslashEnter(data, kb) {
			e.backspace()
			e.submitValue()
			return
		}
		e.insertNewline()
		e.refreshAutocomplete()
	case kb.Matches(data, KBInputSubmit):
		if e.DisableSubmit {
			return
		}
		line := e.lines[e.cursor[0]]
		if e.cursor[1] > 0 && line[e.cursor[1]-1] == '\\' {
			e.backspace()
			e.insertNewline()
			e.refreshAutocomplete()
			return
		}
		e.submitValue()
	case kb.Matches(data, KBEditorDeleteWordBack):
		// alt+backspace / ctrl+w: delete word backward
		e.deleteWordBackward()
		e.refreshAutocomplete()
	case kb.Matches(data, KBEditorDeleteWordForward):
		// alt+d / alt+delete: delete word forward
		e.deleteWordForward()
		e.refreshAutocomplete()
	case kb.Matches(data, KBEditorDeleteCharBack):
		// backspace: delete char backward
		e.backspace()
		e.refreshAutocomplete()
	case kb.Matches(data, KBEditorDeleteCharForward):
		// Delete key / Ctrl+D: forward delete
		e.forwardDelete()
		e.refreshAutocomplete()
	case kb.Matches(data, KBEditorCursorLineStart):
		// Home / Ctrl+A: start of line
		e.lastAction = ""
		e.cursor[1] = 0
		e.Invalidate()
	case kb.Matches(data, KBEditorCursorLineEnd):
		// End / Ctrl+E: end of line
		e.lastAction = ""
		e.cursor[1] = len(e.lines[e.cursor[0]])
		e.Invalidate()
	case kb.Matches(data, KBEditorDeleteToLineStart):
		// Ctrl+U: kill to line start
		e.deleteToLineStart()
		e.refreshAutocomplete()
	case kb.Matches(data, KBEditorDeleteToLineEnd):
		// Ctrl+K: kill to end of line
		e.deleteToLineEnd()
		e.refreshAutocomplete()
	case kb.Matches(data, KBEditorYank):
		// Ctrl+Y: yank
		if e.killRing.Len() > 0 {
			e.saveHistory()
			text := e.killRing.Peek()
			e.yankStart = e.cursor
			e.yankLen = len(text)
			e.insert(text)
			e.lastAction = "yank"
		}
	case kb.Matches(data, KBEditorYankPop):
		// Alt+Y: yank-pop; cycles kill ring after a yank
		if e.lastAction == "yank" && e.killRing.Len() > 1 {
			e.saveHistory()
			// Delete the previously yanked text.
			e.deleteYankedText()
			// Rotate: move last entry to front; new Peek is the previous second.
			e.killRing.Rotate()
			// Re-insert the new top.
			text := e.killRing.Peek()
			e.yankStart = e.cursor
			e.yankLen = len(text)
			e.insert(text)
			e.lastAction = "yank"
		}
	case kb.Matches(data, KBEditorUndo):
		// Ctrl+- (default): undo. `ctrl+-` emits \x1f on most terminals;
		// user can rebind via
		// ~/.wopr/keybindings.json.
		e.lastAction = ""
		e.undo()
	case kb.Matches(data, KBEditorPageUp):
		e.pageScroll(-1)
	case kb.Matches(data, KBEditorPageDown):
		e.pageScroll(1)
	case kb.Matches(data, KBEditorJumpForward):
		e.jumpMode = "forward"
	case kb.Matches(data, KBEditorJumpBackward):
		e.jumpMode = "backward"
	default:
		if len(data) > 0 && data[0] >= 0x20 {
			e.lastAction = ""
			e.insert(data)
			e.refreshAutocomplete()
		} else if ch, ok := DecodePrintableKey(data); ok {
			// Kitty keyboard mode and xterm modifyOtherKeys report Shift+<letter>
			// as an escape sequence. Decode it to the actual character so capital
			// letters are not silently dropped. Same helper TextInput uses.
			e.lastAction = ""
			e.insert(ch)
			e.refreshAutocomplete()
		}
	}
}

// actionsBeforeHistory are the editor actions HandleInput checks before
// tui.editor.historyPrevious/historyNext.
var actionsBeforeHistory = []TUIKeybinding{
	KBInputCopy, KBEditorUndo, KBInputTab,
	KBEditorDeleteToLineEnd, KBEditorDeleteToLineStart,
	KBEditorDeleteWordBack, KBEditorDeleteWordForward,
	KBEditorDeleteCharBack, KBEditorDeleteCharForward,
	KBEditorYank, KBEditorYankPop,
}

func (e *Editor) matchesActionBeforeHistory(kb *TUIKeybindingsManager, data string) bool {
	for _, action := range actionsBeforeHistory {
		if kb.Matches(data, action) {
			return true
		}
	}
	return false
}

func matchesEditorNewLine(data string, kb *TUIKeybindingsManager) bool {
	return kb.Matches(data, KBInputNewLine) ||
		len(data) > 1 && data[0] == '\n' ||
		data == "\x1b\r" ||
		data == "\x1b[13;2~" ||
		len(data) > 1 && strings.Contains(data, "\x1b") && strings.Contains(data, "\r") ||
		data == "\n"
}

func (e *Editor) shouldSubmitOnBackslashEnter(data string, kb *TUIKeybindingsManager) bool {
	if e.DisableSubmit || !matchesKeyID(data, "enter") {
		return false
	}
	submitKeys := kb.Keys(KBInputSubmit)
	hasShiftEnter := slices.Contains(submitKeys, "shift+enter") || slices.Contains(submitKeys, "shift+return")
	if !hasShiftEnter {
		return false
	}
	line := e.lines[e.cursor[0]]
	return e.cursor[1] > 0 && line[e.cursor[1]-1] == '\\'
}

func (e *Editor) submitValue() {
	e.AutocompleteCancel()
	result := strings.TrimSpace(e.GetExpandedText())
	e.lines = []string{""}
	e.cursor = [2]int{0, 0}
	e.jumpMode = ""
	e.preferredVisualCol = nil
	e.snappedFromCursorCol = nil
	e.inputHistIdx = -1
	e.scrollOffset = 0
	e.pastes = nil
	e.pasteCounter = 0
	e.lastAction = ""
	e.history = nil
	e.histIdx = 0
	e.saveHistory()
	e.Invalidate()
	if e.OnSubmit != nil {
		e.OnSubmit(result)
	}
}

func (e *Editor) insert(s string) {
	// Handle multi-line inserts (e.g. from paste or programmatic API).
	if strings.Contains(s, "\n") {
		e.insertMultiLine(s)
		return
	}
	l := e.cursor[0]
	c := e.cursor[1]
	line := e.lines[l]
	e.lines[l] = line[:c] + s + line[c:]
	e.cursor[1] += len(s)
	e.inputHistIdx = -1 // typing exits history-browse mode
	e.saveHistory()
	e.Invalidate()
}

// insertMultiLine inserts text that may contain newlines.
func (e *Editor) insertMultiLine(s string) {
	parts := strings.Split(s, "\n")
	l := e.cursor[0]
	c := e.cursor[1]
	line := e.lines[l]
	before := line[:c]
	after := line[c:]

	// First part joins with content before cursor.
	e.lines[l] = before + parts[0]

	// Middle parts become new lines.
	for i := 1; i < len(parts)-1; i++ {
		e.lines = append(e.lines[:l+i], append([]string{parts[i]}, e.lines[l+i:]...)...)
	}

	// Last part gets the content after the cursor.
	lastIdx := l + len(parts) - 1
	if len(parts) > 1 {
		e.lines = append(e.lines[:lastIdx], append([]string{parts[len(parts)-1] + after}, e.lines[lastIdx:]...)...)
	} else {
		e.lines[l] += after
	}

	e.cursor[0] = lastIdx
	e.cursor[1] = len(parts[len(parts)-1])
	e.inputHistIdx = -1
	e.saveHistory()
	e.Invalidate()
}

// InsertTextAtCursor is the public API for programmatic text insertion.
// Handles multi-line text.
func (e *Editor) InsertTextAtCursor(text string) {
	e.saveHistory()
	e.insert(text)
	e.refreshAutocomplete()
	if e.OnChange != nil {
		e.OnChange(e.Text())
	}
}

func (e *Editor) insertNewline() {
	l := e.cursor[0]
	c := e.cursor[1]
	line := e.lines[l]
	before := line[:c]
	after := line[c:]
	e.lines = append(e.lines[:l+1], append([]string{after}, e.lines[l+1:]...)...)
	e.lines[l] = before
	e.cursor[0]++
	e.cursor[1] = 0
	e.saveHistory()
	e.Invalidate()
}

func (e *Editor) backspace() {
	l := e.cursor[0]
	c := e.cursor[1]
	if c > 0 {
		line := e.lines[l]
		// If the segment immediately before the cursor is a paste marker,
		// delete the whole marker atomically and compact the paste registry.
		// Otherwise delete one grapheme.
		if segs := e.segmentLine(line[:c]); len(segs) > 0 && isPasteMarker(segs[len(segs)-1].Text) {
			marker := segs[len(segs)-1]
			e.lines[l] = line[:marker.Start] + line[c:]
			e.cursor[1] = marker.Start
			if m := pasteMarkerSingle.FindStringSubmatch(marker.Text); m != nil {
				if targetID, err := strconv.Atoi(m[1]); err == nil {
					e.removePasteFromRegistry(targetID)
				}
			}
		} else {
			start := previousGraphemeStart(line, c)
			e.lines[l] = line[:start] + line[c:]
			e.cursor[1] = start
		}
	} else if l > 0 {
		prev := e.lines[l-1]
		e.cursor[1] = len(prev)
		e.lines[l-1] = prev + e.lines[l]
		e.lines = append(e.lines[:l], e.lines[l+1:]...)
		e.cursor[0]--
	}
	e.saveHistory()
	e.Invalidate()
}

// removePasteFromRegistry deletes targetID from the paste registry, shifts
// every higher id down by one, and renumbers the surviving [paste #N] markers
// in the buffer text so ids stay contiguous (deleting [paste #1] renumbers
// [paste #2] to [paste #1], and so on, independent of marker order in the
// text).
func (e *Editor) removePasteFromRegistry(targetID int) {
	if e.pastes != nil {
		delete(e.pastes, targetID)
		higher := make([]int, 0, len(e.pastes))
		for id := range e.pastes {
			if id > targetID {
				higher = append(higher, id)
			}
		}
		slices.Sort(higher)
		for _, id := range higher {
			e.pastes[id-1] = e.pastes[id]
			delete(e.pastes, id)
		}
	}
	e.pasteCounter--
	for i, ln := range e.lines {
		e.lines[i] = pasteMarkerRegex.ReplaceAllStringFunc(ln, func(marker string) string {
			m := pasteMarkerRegex.FindStringSubmatch(marker)
			if m == nil {
				return marker
			}
			id, err := strconv.Atoi(m[1])
			if err != nil || id <= targetID {
				return marker
			}
			return "[paste #" + strconv.Itoa(id-1) + m[2] + "]"
		})
	}
}

// forwardDelete deletes the character at the cursor (Delete key / Ctrl+D).
func (e *Editor) forwardDelete() {
	l := e.cursor[0]
	c := e.cursor[1]
	line := e.lines[l]
	if c < len(line) {
		end := nextGraphemeEnd(line, c)
		e.lines[l] = line[:c] + line[end:]
	} else if l < len(e.lines)-1 {
		// Join with next line.
		e.lines[l] = line + e.lines[l+1]
		e.lines = append(e.lines[:l+1], e.lines[l+2:]...)
	}
	e.saveHistory()
	e.Invalidate()
}

func (e *Editor) moveToVisualLine(visual []editorVisualLine, currentVisualLine, targetVisualLine int) {
	if currentVisualLine < 0 || currentVisualLine >= len(visual) || targetVisualLine < 0 || targetVisualLine >= len(visual) {
		return
	}
	currentVL := visual[currentVisualLine]
	targetVL := visual[targetVisualLine]
	currentVisualCol := e.cursor[1] - currentVL.startCol
	if e.snappedFromCursorCol != nil {
		vlIndex := e.findVisualLineAt(visual, currentVL.logicalLine, *e.snappedFromCursorCol)
		currentVisualCol = *e.snappedFromCursorCol - visual[vlIndex].startCol
	}

	isLastSourceSegment := currentVisualLine == len(visual)-1 || visual[currentVisualLine+1].logicalLine != currentVL.logicalLine
	sourceMaxVisualCol := currentVL.length
	if !isLastSourceSegment {
		sourceMaxVisualCol = max(0, currentVL.length-1)
	}

	isLastTargetSegment := targetVisualLine == len(visual)-1 || visual[targetVisualLine+1].logicalLine != targetVL.logicalLine
	targetMaxVisualCol := targetVL.length
	if !isLastTargetSegment {
		targetMaxVisualCol = max(0, targetVL.length-1)
	}

	moveToVisualCol := e.computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol)
	e.cursor[0] = targetVL.logicalLine
	targetCol := targetVL.startCol + moveToVisualCol
	e.cursor[1] = min(targetCol, len(e.lines[targetVL.logicalLine]))
	e.snappedFromCursorCol = nil
	e.Invalidate()
}

func (e *Editor) computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol int) int {
	hasPreferred := e.preferredVisualCol != nil
	cursorInMiddle := currentVisualCol < sourceMaxVisualCol
	targetTooShort := targetMaxVisualCol < currentVisualCol

	if !hasPreferred || cursorInMiddle {
		if targetTooShort {
			e.preferredVisualCol = new(currentVisualCol)
			return targetMaxVisualCol
		}
		e.preferredVisualCol = nil
		return currentVisualCol
	}

	targetCantFitPreferred := targetMaxVisualCol < *e.preferredVisualCol
	if targetTooShort || targetCantFitPreferred {
		return targetMaxVisualCol
	}

	result := *e.preferredVisualCol
	e.preferredVisualCol = nil
	return result
}

func (e *Editor) moveCursor(deltaLine, deltaCol int) {
	e.lastAction = ""
	visual := e.buildVisualLineMap(e.renderWidth)
	currentVisualLine := e.findCurrentVisualLine(visual)

	if deltaLine != 0 {
		targetVisualLine := currentVisualLine + deltaLine
		if targetVisualLine >= 0 && targetVisualLine < len(visual) {
			e.moveToVisualLine(visual, currentVisualLine, targetVisualLine)
		}
	}

	if deltaCol == 0 {
		return
	}
	currentLine := e.lines[e.cursor[0]]
	if deltaCol > 0 {
		switch {
		case e.cursor[1] < len(currentLine):
			e.setCursorCol(nextGraphemeEnd(currentLine, e.cursor[1]))
		case e.cursor[0] < len(e.lines)-1:
			e.cursor[0]++
			e.setCursorCol(0)
		case currentVisualLine >= 0 && currentVisualLine < len(visual):
			currentVL := visual[currentVisualLine]
			e.preferredVisualCol = new(e.cursor[1] - currentVL.startCol)
		}
	} else {
		switch {
		case e.cursor[1] > 0:
			e.setCursorCol(previousGraphemeStart(currentLine, e.cursor[1]))
		case e.cursor[0] > 0:
			e.cursor[0]--
			e.setCursorCol(len(e.lines[e.cursor[0]]))
		}
	}
	e.Invalidate()
}

func (e *Editor) pageScroll(direction int) {
	e.lastAction = ""
	visual := e.buildVisualLineMap(e.renderWidth)
	currentVisualLine := e.findCurrentVisualLine(visual)
	pageSize := max(5, e.maxVisibleLines)
	targetVisualLine := currentVisualLine + direction*pageSize
	targetVisualLine = max(0, min(len(visual)-1, targetVisualLine))
	e.moveToVisualLine(visual, currentVisualLine, targetVisualLine)
}

func (e *Editor) jumpToChar(char, direction string) {
	e.lastAction = ""
	forward := direction == "forward"
	end := -1
	step := -1
	if forward {
		end = len(e.lines)
		step = 1
	}
	for lineIdx := e.cursor[0]; lineIdx != end; lineIdx += step {
		line := e.lines[lineIdx]
		searchFrom := 0
		if forward {
			if lineIdx == e.cursor[0] {
				searchFrom = min(e.cursor[1]+1, len(line))
			}
		} else {
			searchFrom = len(line) - 1
			if lineIdx == e.cursor[0] {
				searchFrom = e.cursor[1] - 1
			}
		}
		var idx int
		if forward {
			idx = strings.Index(line[searchFrom:], char)
			if idx >= 0 {
				idx += searchFrom
			}
		} else {
			if searchFrom < 0 {
				continue
			}
			idx = strings.LastIndex(line[:searchFrom+1], char)
		}
		if idx >= 0 {
			e.cursor[0] = lineIdx
			e.setCursorCol(idx)
			e.Invalidate()
			return
		}
	}
}

func (e *Editor) undo() {
	// Undo leaves history browsing.
	e.inputHistIdx = -1
	e.inputHistSaved = nil
	if e.histIdx > 1 {
		e.histIdx--
		state := e.history[e.histIdx-1]
		e.lines = make([]string, len(state.lines))
		copy(e.lines, state.lines)
		e.cursor = state.cursor
		e.pastes = clonePastes(state.pastes)
		e.pasteCounter = state.pasteCounter
		e.Invalidate()
	}
}

// ─── Word-boundary navigation + deletion ─────────────────────────
//
// Word boundary semantics: skip whitespace, then consume one punctuation run
// OR one non-whitespace/non-punctuation word run. These operations may cross
// line boundaries by deleting or traversing a newline at line start/end.

// Word navigation classifies by grapheme (whitespace/ASCII-punct/word), not
// UAX#29 word segments, so a CJK run with no separators is one word.
func prevWordStart(line string, c int) int {
	if c <= 0 {
		return 0
	}
	segs := graphemeSegments(line[:c])
	newCol := c
	for len(segs) > 0 && isWhitespaceGrapheme(segs[len(segs)-1].Text) {
		newCol -= len(segs[len(segs)-1].Text)
		segs = segs[:len(segs)-1]
	}
	if len(segs) == 0 {
		return newCol
	}
	if isPunctuationGrapheme(segs[len(segs)-1].Text) {
		for len(segs) > 0 && isPunctuationGrapheme(segs[len(segs)-1].Text) {
			newCol -= len(segs[len(segs)-1].Text)
			segs = segs[:len(segs)-1]
		}
		return newCol
	}
	for len(segs) > 0 && !isWhitespaceGrapheme(segs[len(segs)-1].Text) && !isPunctuationGrapheme(segs[len(segs)-1].Text) {
		newCol -= len(segs[len(segs)-1].Text)
		segs = segs[:len(segs)-1]
	}
	return newCol
}

// Grapheme-class word nav (see prevWordStart).
func nextWordEnd(line string, c int) int {
	n := len(line)
	if c >= n {
		return n
	}
	newCol := c
	segs := graphemeSegments(line[c:])
	for len(segs) > 0 && isWhitespaceGrapheme(segs[0].Text) {
		newCol += len(segs[0].Text)
		segs = segs[1:]
	}
	if len(segs) == 0 {
		return newCol
	}
	if isPunctuationGrapheme(segs[0].Text) {
		for len(segs) > 0 && isPunctuationGrapheme(segs[0].Text) {
			newCol += len(segs[0].Text)
			segs = segs[1:]
		}
		return newCol
	}
	for len(segs) > 0 && !isWhitespaceGrapheme(segs[0].Text) && !isPunctuationGrapheme(segs[0].Text) {
		newCol += len(segs[0].Text)
		segs = segs[1:]
	}
	return newCol
}

func (e *Editor) cursorWordBackward() {
	l := e.cursor[0]
	c := e.cursor[1]
	if c == 0 {
		if l > 0 {
			e.cursor[0]--
			e.cursor[1] = len(e.lines[e.cursor[0]])
			e.Invalidate()
		}
		return
	}
	e.cursor[1] = prevWordStart(e.lines[l], c)
	e.Invalidate()
}

func (e *Editor) cursorWordForward() {
	l := e.cursor[0]
	c := e.cursor[1]
	if c >= len(e.lines[l]) {
		if l < len(e.lines)-1 {
			e.cursor[0]++
			e.cursor[1] = 0
			e.Invalidate()
		}
		return
	}
	e.cursor[1] = nextWordEnd(e.lines[l], c)
	e.Invalidate()
}

func (e *Editor) deleteWordBackward() {
	l := e.cursor[0]
	c := e.cursor[1]
	if c == 0 {
		if l == 0 {
			return
		}
		wasKill := e.lastAction == "kill"
		e.killRing.Push("\n", true, wasKill)
		prev := e.lines[l-1]
		e.cursor[0] = l - 1
		e.cursor[1] = len(prev)
		e.lines[l-1] = prev + e.lines[l]
		e.lines = append(e.lines[:l], e.lines[l+1:]...)
		e.lastAction = "kill"
		e.saveHistory()
		e.Invalidate()
		return
	}
	line := e.lines[l]
	start := prevWordStart(line, c)
	killed := line[start:c]
	wasKill := e.lastAction == "kill"
	e.killRing.Push(killed, true, wasKill)
	e.lastAction = "kill"
	e.lines[l] = line[:start] + line[c:]
	e.cursor[1] = start
	e.saveHistory()
	e.Invalidate()
}

func (e *Editor) deleteWordForward() {
	l := e.cursor[0]
	c := e.cursor[1]
	line := e.lines[l]
	if c >= len(line) {
		if l >= len(e.lines)-1 {
			return
		}
		wasKill := e.lastAction == "kill"
		e.killRing.Push("\n", false, wasKill)
		e.lines[l] = line + e.lines[l+1]
		e.lines = append(e.lines[:l+1], e.lines[l+2:]...)
		e.lastAction = "kill"
		e.saveHistory()
		e.Invalidate()
		return
	}
	end := nextWordEnd(line, c)
	killed := line[c:end]
	wasKill := e.lastAction == "kill"
	e.killRing.Push(killed, false, wasKill)
	e.lastAction = "kill"
	e.lines[l] = line[:c] + line[end:]
	e.saveHistory()
	e.Invalidate()
}

// deleteToLineEnd kills from cursor to end of the current line.
// If cursor is already at end-of-line and this is not the last line,
// kills the newline (joining with next line).
func (e *Editor) deleteToLineEnd() {
	l := e.cursor[0]
	c := e.cursor[1]
	wasKill := e.lastAction == "kill"
	if c < len(e.lines[l]) {
		killed := e.lines[l][c:]
		e.killRing.Push(killed, false, wasKill)
		e.lines[l] = e.lines[l][:c]
		e.lastAction = "kill"
		e.saveHistory()
		e.Invalidate()
	} else if l < len(e.lines)-1 {
		// At end-of-line but not last line: kill the newline.
		e.killRing.Push("\n", false, wasKill)
		e.lines[l] += e.lines[l+1]
		e.lines = append(e.lines[:l+1], e.lines[l+2:]...)
		e.lastAction = "kill"
		e.saveHistory()
		e.Invalidate()
	}
}

// deleteToLineStart kills from line start to cursor.
// If cursor is at col 0 and not on first line, kills the newline.
func (e *Editor) deleteToLineStart() {
	l := e.cursor[0]
	c := e.cursor[1]
	wasKill := e.lastAction == "kill"
	if c > 0 {
		killed := e.lines[l][:c]
		e.killRing.Push(killed, true, wasKill)
		e.lines[l] = e.lines[l][c:]
		e.cursor[1] = 0
		e.lastAction = "kill"
		e.saveHistory()
		e.Invalidate()
	} else if l > 0 {
		// At col 0 but not first line: kill the newline.
		e.killRing.Push("\n", true, wasKill)
		prev := e.lines[l-1]
		e.cursor[1] = len(prev)
		e.lines[l-1] = prev + e.lines[l]
		e.lines = append(e.lines[:l], e.lines[l+1:]...)
		e.cursor[0]--
		e.lastAction = "kill"
		e.saveHistory()
		e.Invalidate()
	}
}

// deleteYankedText removes the previously yanked text (used by yank-pop).
// Deletes e.yankLen bytes ending at current cursor position on the current line.
// Only handles single-line yanks; multi-line yank-pop is deferred (uncommon).
func (e *Editor) deleteYankedText() {
	l := e.cursor[0]
	c := e.cursor[1]
	if e.yankLen <= 0 || c < e.yankLen {
		return
	}
	line := e.lines[l]
	e.lines[l] = line[:c-e.yankLen] + line[c:]
	e.cursor[1] = c - e.yankLen
	e.yankLen = 0
	e.Invalidate()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// wrapText wraps s to lines of at most width runes.
// wrapText wraps s to lines of at most `width` visible columns, preserving
// active ANSI/OSC 8 hyperlink state across line breaks.
//
// Delegates to widthx.WrapTextWithAnsi. The previous in-place implementation
// used `len(stripANSI(line))` for width measurement which was wrong for
// CJK/emoji and could not break overlong tokens.
func wrapText(s string, width int) []string {
	return widthx.WrapTextWithAnsi(s, width)
}

// WrapText wraps text at width, preserving ANSI escape codes across
// line breaks. Exported for use by tool renderers.
func WrapText(s string, width int) []string {
	return widthx.WrapTextWithAnsi(s, width)
}
