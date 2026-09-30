package tui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ToolExecutionState is the lifecycle stage of a tool call display.
type ToolExecutionState int

const (
	ToolStateRunning ToolExecutionState = iota
	ToolStateDone
	ToolStateError
)

// ToolExecutionComponent renders one tool call in the chat transcript.
//
// Visual model:
//
//	$ expr 20 + 22             ← bash: bold "$ <command>"
//	read README.md             ← read: bold "read <path>"
//	write out.txt              ← write: bold "write <path>"
//	edit main.go               ← edit: bold "edit <path>"
//	grep /pattern/ in .           ← grep: bold "grep /<pat>/ in <path>"
//	find *.go in .             ← find: bold "find <pat> in <path>"
//	ls src/                    ← ls: bold "ls <path>"
//
// No lifecycle markers (✓/▶/✗): state is conveyed only via
// background color (pending, success, error). No "(N lines, Xs)"
// annotation in the header: duration shows in the body footer.
//
// State transitions:
//   - SetRunning: state → Running, output cleared
//   - SetResult:  state → Done or Error, body filled
//
// Output thresholds:
//   - autoCollapseLines: outputs longer than this start collapsed
//     (overridden to expanded for errors)
//   - bodyMaxLines: lines shown in the expanded view; overflow shows a
//     "\u2026 (N more lines)" footer line.
type ToolExecutionComponent struct {
	invalidatable

	Name  string
	Label string // human-readable display name (if set, used in header instead of Name)
	// ArgsPreview is a short human-readable rendering of the tool args.
	// Build it with FormatToolArgs() before assigning, or use SetRunning().
	ArgsPreview string

	// Cwd is the session working directory, used to resolve relative tool
	// paths to absolute file:// URLs for OSC-8 hyperlinks in the header.
	Cwd string

	State  ToolExecutionState
	Output string

	// Collapsed controls the renderer's expanded state. New tool cards start
	// collapsed; final results may apply tool-specific rules.
	Collapsed bool

	// Elapsed is the finished call's duration.
	Elapsed time.Duration

	// StartedAt records when the tool began executing; the running spinner's
	// frame is derived from the time since it.
	StartedAt time.Time

	// Subline, when set, renders under an inline card as "↳ <Subline>"
	// (a task's current tool call, then its tool calls, duration, and
	// model). SubWarn colors it as a warning.
	Subline string
	SubWarn bool

	// Configurable thresholds. Zero values fall back to defaults.
	AutoCollapseLines int // default 8

	// Render cache: short-circuits Render() when nothing changed.
	cachedState       ToolExecutionState
	cachedOutput      string
	cachedCollapsed   bool
	cachedWidth       int
	cachedIsPartial   bool
	cachedArgsPreview string
	cachedLines       []string
	cachedTheme       *Theme
	cachedArgs        string
	cachedSeparated   bool
	cachedSubline     string

	// args is the call's raw JSON arguments; previous is the transcript item
	// above the card. See tool_card.go.
	args     json.RawMessage
	previous Component

	// BodyRenderer, when non-nil, replaces the default plain-text body
	// rendering. Used for per-tool rich displays: unified diff for
	// edit, line-numbered output for read, etc. The function receives
	// the available width and the current expanded state so it can show
	// a truncated preview or full output depending on Ctrl+O toggle.
	//
	// When set, the line-count annotation in the header is computed
	// from the renderer's row count instead of the raw Output text so
	// `(N lines, 1.2s)` accurately reflects what the user can see.
	BodyRenderer func(width int, expanded bool) []string

	// ShellChanges renders, for a shell tool, the files the command changed
	// with their diffs; the card appends it below the command's output.
	ShellChanges func(width int, expanded bool) []string

	// ImageBlocks holds image content blocks from tool results.
	// When non-empty and ShowImages is true, they render after the body.
	ImageBlocks []ImageBlock

	// ShowImages controls whether ImageBlocks are rendered.
	ShowImages bool

	// ImageWidthCells caps the width of rendered images in columns (default 60).
	ImageWidthCells int

	// convertedImages caches Kitty PNG conversions by image index, keyed to
	// the source block they were made from.
	convertedImages map[int]convertedToolImage

	// userToggled is true once the user has explicitly hit the toggle
	// key. After that we never re-apply auto-collapse, so a user who
	// expanded a long output doesn't lose it when SetResult re-fires.
	userToggled bool

	// IsPartial is true while
	// the tool is still being streamed/executed, false after final result.
	// Controls the pending bg tint before execution completes.
	IsPartial bool

	// executionStarted is set when ToolExecutionStartEvent fires (tool begins executing).
	executionStarted bool

	// argsComplete is set when the message stream ends and args JSON is finalized.
	argsComplete bool
}

// ImageBlock describes one image from a tool result for rendering.
type ImageBlock struct {
	Data     string // base64-encoded image data
	MIMEType string
}

// ConvertedImage is a base64 image and its MIME type, the result of a PNG
// conversion.
type ConvertedImage struct {
	Data     string
	MimeType string
}

// convertedToolImage is one convertedImages entry: the conversion plus the
// source block it came from.
type convertedToolImage struct {
	sourceData     string
	sourceMimeType string
	ConvertedImage
}

// KittyImageConversion names one tool-result image that needs a PNG
// conversion before the Kitty graphics protocol can display it.
type KittyImageConversion struct {
	Index    int
	Data     string
	MimeType string
}

// PendingKittyImageConversions selects images that need PNG conversion: on a
// Kitty terminal it returns every image block
// with data and a MIME type that is not PNG and has no conversion cached for
// its current source. The caller converts each one off the UI loop and hands
// the result to ApplyConvertedImage on the loop.
func (c *ToolExecutionComponent) PendingKittyImageConversions() []KittyImageConversion {
	if Capabilities().Images != ImageProtocolKitty {
		return nil
	}
	var pending []KittyImageConversion
	for i, img := range c.ImageBlocks {
		if img.Data == "" || img.MIMEType == "" || img.MIMEType == "image/png" {
			continue
		}
		if cached, ok := c.convertedImages[i]; ok && cached.sourceData == img.Data && cached.sourceMimeType == img.MIMEType {
			continue
		}
		pending = append(pending, KittyImageConversion{Index: i, Data: img.Data, MimeType: img.MIMEType})
	}
	return pending
}

// ApplyConvertedImage stores a finished PNG conversion. A failed conversion
// (nil) or one that finishes after its image block was replaced is ignored;
// otherwise the conversion is cached and the component invalidated. It reports
// whether the conversion was applied, so the caller knows to request a render.
func (c *ToolExecutionComponent) ApplyConvertedImage(req KittyImageConversion, converted *ConvertedImage) bool {
	if converted == nil || req.Index < 0 || req.Index >= len(c.ImageBlocks) {
		return false
	}
	current := c.ImageBlocks[req.Index]
	if current.Data != req.Data || current.MIMEType != req.MimeType {
		return false
	}
	if c.convertedImages == nil {
		c.convertedImages = make(map[int]convertedToolImage)
	}
	c.convertedImages[req.Index] = convertedToolImage{
		sourceData:     req.Data,
		sourceMimeType: req.MimeType,
		ConvertedImage: *converted,
	}
	c.cachedLines = nil
	c.Invalidate()
	return true
}

// NewToolExecutionComponent returns a Running-state component for the
// given tool. Args may be empty.
func NewToolExecutionComponent(name, argsPreview string) *ToolExecutionComponent {
	return &ToolExecutionComponent{
		Name:            name,
		ArgsPreview:     argsPreview,
		State:           ToolStateRunning,
		Collapsed:       true,
		ShowImages:      true,
		ImageWidthCells: 60,
		IsPartial:       true,
	}
}

// IsDirty reports whether the component needs re-rendering. While a tool
// runs, its spinner frame advances with time.Since(StartedAt) on every frame
// driven by the tick loop, but the tick does not Invalidate this component.
// Reporting dirty while running keeps the per-child render cache from
// freezing the spinner.
func (c *ToolExecutionComponent) IsDirty() bool {
	if c.invalidatable.IsDirty() {
		return true
	}
	return c.State == ToolStateRunning
}

// SetRunning marks the component as in-flight with the given pre-formatted
// args preview. Idempotent.
func (c *ToolExecutionComponent) SetRunning(argsPreview string) {
	c.State = ToolStateRunning
	c.ArgsPreview = argsPreview
	c.Output = ""
	c.Elapsed = 0
	c.Invalidate()
}

// SetStreaming updates the live output body during execution without changing
// expansion state. Only SetExpanded or Toggle changes that state while running.
func (c *ToolExecutionComponent) SetStreaming(snapshot string) {
	if c.State != ToolStateRunning {
		return
	}
	c.Output = snapshot
	c.Invalidate()
}

// UpdateArgs updates the displayed header from partial/complete args.
// Called progressively during streaming as ToolCallDelta events arrive.
func (c *ToolExecutionComponent) UpdateArgs(name string, partialArgsJSON string) {
	if name != "" {
		c.Name = name
	}
	// Try to parse the partial JSON to get a header. Partial JSON will
	// fail to parse: that's OK, we fall back to the tool name.
	var raw json.RawMessage
	if json.Unmarshal([]byte(partialArgsJSON), &raw) == nil {
		c.args = append(c.args[:0], raw...)
		c.ArgsPreview = HeaderForTool(c.Name, raw, c.Cwd)
	}
	c.Invalidate()
}

// MarkExecutionStarted records that the tool has begun executing.
func (c *ToolExecutionComponent) MarkExecutionStarted() {
	c.executionStarted = true
	if c.StartedAt.IsZero() {
		c.StartedAt = time.Now()
	}
	c.Invalidate()
}

// SetArgsComplete records that the args JSON is finalized.
func (c *ToolExecutionComponent) SetArgsComplete() {
	c.argsComplete = true
	c.Invalidate()
}

// SetResult finalises the component with output text and an error flag,
// applying the auto-collapse rule unless the user has already toggled.
func (c *ToolExecutionComponent) SetResult(output string, isError bool, elapsed time.Duration) {
	c.IsPartial = false
	if isError {
		c.State = ToolStateError
	} else {
		c.State = ToolStateDone
	}
	c.Output = output
	c.Elapsed = elapsed
	if !c.userToggled {
		switch {
		case isError:
			// Errors are always auto-expanded: the LLM (and the user) need
			// to see what went wrong without an extra keystroke.
			c.Collapsed = false
		case c.BodyRenderer != nil:
			// Tools with BodyRenderer handle their own preview/expanded toggle.
			c.Collapsed = true
		case c.Name == "task":
			// A task stays a one-line row with its summary; Ctrl+O shows
			// the checked result.
			c.Collapsed = true
		case HasBuiltInToolRenderers(c.Name):
			// Built-ins receive their renderer before final delivery in production.
			// Keep the line-count fallback for direct/component-only callers.
			c.Collapsed = c.lineCount() > c.autoCollapseThreshold()
		default:
			// Show the complete output when no tool definition exists.
			c.Collapsed = false
		}
	}
	c.Invalidate()
}

// FinalizeAborted freezes a still-running tool when its turn is aborted
// mid-execution. It transitions out of Running so the spinner stops
// forcing a re-render on every subsequent frame
// (which otherwise forced a repaint on every keystroke and agent chunk,
// breaking terminal scrollback) while keeping any partial streamed output.
// No-op if the tool already reached a terminal state.
func (c *ToolExecutionComponent) FinalizeAborted(elapsed time.Duration) {
	if c.State != ToolStateRunning {
		return
	}
	out := c.Output
	if strings.TrimSpace(out) == "" {
		out = "Operation aborted"
	}
	c.SetResult(out, true, elapsed)
}

// SetExpanded forces the body open or closed and records the user's
// intent so subsequent SetResult calls don't snap it back. Used by
// global Ctrl+O (toggle-all-tools) so every component lands in the
// same state.
func (c *ToolExecutionComponent) SetExpanded(expanded bool) {
	c.Collapsed = !expanded
	c.userToggled = true
	c.Invalidate()
}

// Toggle flips the collapsed state and records that the user touched it
// so subsequent SetResult calls don't snap it back.
func (c *ToolExecutionComponent) Toggle() {
	c.Collapsed = !c.Collapsed
	c.userToggled = true
	c.Invalidate()
}

// Expand forces the body open.
func (c *ToolExecutionComponent) Expand() {
	c.Collapsed = false
	c.userToggled = true
	c.Invalidate()
}

// Collapse forces the body closed.
func (c *ToolExecutionComponent) Collapse() {
	c.Collapsed = true
	c.userToggled = true
	c.Invalidate()
}

// SetShowImages toggles image rendering.
func (c *ToolExecutionComponent) SetShowImages(show bool) {
	c.ShowImages = show
	c.Invalidate()
}

// SetImageWidthCells updates the max image width.
func (c *ToolExecutionComponent) SetImageWidthCells(width int) {
	c.ImageWidthCells = max(1, width)
	c.Invalidate()
}

// renderImages renders image blocks after the tool body.
func (c *ToolExecutionComponent) renderImages(width int) []string {
	if !c.ShowImages || len(c.ImageBlocks) == 0 {
		return nil
	}
	caps := Capabilities()
	if caps.Images == "" {
		// No image protocol: render fallback text for each image.
		var out []string
		for _, img := range c.ImageBlocks {
			out = append(out, "") // spacer
			dims := GetImageDimensions(img.Data, img.MIMEType)
			out = append(out, ImageFallback(img.MIMEType, dims, ""))
		}
		return out
	}
	maxW := min(width-2, c.ImageWidthCells)
	if maxW <= 0 {
		maxW = min(width, 60)
	}
	var out []string
	for i, img := range c.ImageBlocks {
		// Prefer a conversion made from this exact
		// source block, and on Kitty skip a non-PNG image entirely (no
		// spacer) until its conversion lands.
		if cached, ok := c.convertedImages[i]; ok && cached.sourceData == img.Data && cached.sourceMimeType == img.MIMEType {
			img = ImageBlock{Data: cached.Data, MIMEType: cached.MimeType}
		}
		if caps.Images == ImageProtocolKitty && img.MIMEType != "image/png" {
			continue
		}
		dims := ImageDimensions{WidthPx: 800, HeightPx: 600}
		if got := GetImageDimensions(img.Data, img.MIMEType); got != nil {
			dims = *got
		}
		out = append(out, "") // spacer between images
		result := RenderImage(img.Data, dims, ImageRenderOptions{
			MaxWidthCells:       maxW,
			PreserveAspectRatio: true,
			Name:                "",
		})
		if result != nil {
			for range max(result.Rows-1, 0) {
				out = append(out, "")
			}
			moveUp := ""
			if result.Rows > 1 {
				moveUp = "\x1b[" + strconv.Itoa(result.Rows-1) + "A"
			}
			out = append(out, moveUp+result.Sequence)
		} else {
			out = append(out, ImageFallback(img.MIMEType, &dims, ""))
		}
	}
	return out
}

// Render emits header + (optional) body lines, all bg-painted in the
// lifecycle color (pending / success / error). Row 2.9a.
//
// Content gets 1-row vertical padding above and below, plus 1-column left
// padding on each content line: top-pad, header, separator, body, bottom-pad: all bg-painted.
func (c *ToolExecutionComponent) Render(width int) []string {
	width = max(width, 3)
	// A running card animates its spinner every frame, so the line cache
	// must not short-circuit it.
	separated := c.separated()
	if c.State != ToolStateRunning && c.cachedLines != nil &&
		c.cachedState == c.State &&
		c.cachedOutput == c.Output &&
		c.cachedCollapsed == c.Collapsed &&
		c.cachedWidth == width &&
		c.cachedIsPartial == c.IsPartial &&
		c.cachedArgsPreview == c.ArgsPreview &&
		c.cachedArgs == string(c.args) &&
		c.cachedSeparated == separated &&
		c.cachedSubline == c.Subline &&
		c.cachedTheme == ActiveTheme() {
		return c.cachedLines
	}
	out := c.renderCard(width)
	out = append(out, c.renderImages(width)...)
	c.saveCachedRender(width, out)
	c.cachedArgs = string(c.args)
	c.cachedSeparated = separated
	return out
}

func (c *ToolExecutionComponent) saveCachedRender(width int, lines []string) {
	c.cachedState = c.State
	c.cachedOutput = c.Output
	c.cachedCollapsed = c.Collapsed
	c.cachedWidth = width
	c.cachedIsPartial = c.IsPartial
	c.cachedArgsPreview = c.ArgsPreview
	c.cachedLines = lines
	c.cachedTheme = ActiveTheme()
	c.cachedSubline = c.Subline
}

// flattenVisualRows splits any element that carries embedded newlines into one
// element per row. Body renderers that wrap long styled lines (e.g. the diff
// renderer's styleAndWrap) join wrapped rows with "\n"; the bg-paint loop
// paints one string per terminal row, so unsplit rows would leave the wrapped
// continuation unpainted at column 0.
func flattenVisualRows(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.IndexByte(l, '\n') < 0 {
			out = append(out, l)
			continue
		}
		out = append(out, strings.Split(l, "\n")...)
	}
	return out
}

func (c *ToolExecutionComponent) lineCount() int {
	if c.Output == "" {
		return 0
	}
	// strings.Count of \n + 1 if the last line lacks a newline.
	n := strings.Count(c.Output, "\n")
	if !strings.HasSuffix(c.Output, "\n") {
		n++
	}
	return n
}

func (c *ToolExecutionComponent) autoCollapseThreshold() int {
	if c.AutoCollapseLines > 0 {
		return c.AutoCollapseLines
	}
	return 8
}

// FormatToolArgs renders a JSON object as a compact `key:val, key:val`
// preview suitable for the tool-call header. Falls back to the raw JSON
// for non-object inputs. Long string values are truncated with "\u2026" so
// the header never overflows the terminal width.
//
// Examples:
//
//	{"path":"x","limit":10}                \u2192  path:"x", limit:10
//	{"command":"git status --porcelain"}    \u2192  command:"git status \u2026"
//	[]                                      \u2192  []
func FormatToolArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Not an object \u2014 just compact-print.
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			return truncateArg(string(b), 80)
		}
		return truncateArg(string(raw), 80)
	}
	keys := slices.Sorted(maps.Keys(obj))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+formatArgValue(obj[k]))
	}
	return truncateArg(strings.Join(parts, ", "), 80)
}

// noEscapeJSON serialises v to JSON without HTML escaping so & < >
// appear as-is in display strings (not as \u0026 \u003c \u003e).
func noEscapeJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(buf.String(), "\n")
}

func formatArgValue(v any) string {
	switch t := v.(type) {
	case string:
		return strconvQuote(truncateArg(t, 40))
	case bool, float64, int, int64:
		return noEscapeJSON(t)
	case nil:
		return "null"
	case []any:
		return fmt.Sprintf("[%d]", len(t))
	case map[string]any:
		return fmt.Sprintf("{%d}", len(t))
	default:
		return noEscapeJSON(t)
	}
}

// strconvQuote wraps strconv.Quote without pulling the import (avoids
// extra surface in this small helper file).
func strconvQuote(s string) string {
	// json.Marshal HTML-escapes &, <, > to \u0026 etc., which leaks into
	// the tool-call header display ("chmod +x foo \u0026\u0026 bar").
	// Use a json.Encoder with SetEscapeHTML(false) to get clean output.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// FormatReadHeader returns the styled `read <path>` header for read tool
// calls: bold toolTitle `read`,
// the path via renderToolPath (accent + ~/ + OSC-8 link), and a warning-
// colored `:start-end` line range.
func FormatReadHeader(raw json.RawMessage, cwd string) string {
	var p struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Offset   *int   `json:"offset,omitempty"`
		Limit    *int   `json:"limit,omitempty"`
	}
	_ = json.Unmarshal(raw, &p)
	path := cmp.Or(p.FilePath, p.Path)
	header := toolTitleText("read") + " " + renderToolPath(path, cwd)
	if p.Offset != nil || p.Limit != nil {
		start := 1
		if p.Offset != nil {
			start = *p.Offset
		}
		rng := fmt.Sprintf(":%d", start)
		if p.Limit != nil {
			rng = fmt.Sprintf(":%d-%d", start, start+*p.Limit-1)
		}
		header += fg(ActiveTheme().Warning, rng)
	}
	return header
}

// FormatWriteHeader returns the styled `write <path>` header for write
// tool calls.
func FormatWriteHeader(raw json.RawMessage, cwd string) string {
	var p struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(raw, &p)
	path := cmp.Or(p.FilePath, p.Path)
	return toolTitleText("write") + " " + renderToolPath(path, cwd)
}

// FormatEditHeader returns the styled `edit <path>` header for edit tool
// calls.
func FormatEditHeader(raw json.RawMessage, cwd string) string {
	var p struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Patch    string `json:"patch"`
		Multi    []struct {
			Path     string `json:"path"`
			FilePath string `json:"file_path"`
		} `json:"multi"`
	}
	_ = json.Unmarshal(raw, &p)
	path := cmp.Or(p.FilePath, p.Path)
	paths := make([]string, 0, len(p.Multi))
	if path != "" {
		paths = append(paths, path)
	}
	for _, edit := range p.Multi {
		editPath := cmp.Or(edit.FilePath, edit.Path)
		if editPath != "" && !slices.Contains(paths, editPath) {
			paths = append(paths, editPath)
		}
	}
	for line := range strings.SplitSeq(p.Patch, "\n") {
		for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if patchPath, ok := strings.CutPrefix(line, prefix); ok {
				patchPath = strings.TrimSpace(patchPath)
				if patchPath != "" && !slices.Contains(paths, patchPath) {
					paths = append(paths, patchPath)
				}
				break
			}
		}
	}
	if len(paths) == 0 {
		return toolTitleText("edit") + " " + renderToolPath("", cwd)
	}
	header := toolTitleText("edit") + " " + renderToolPath(paths[0], cwd)
	if remaining := len(paths) - 1; remaining > 0 {
		header += fg(ActiveTheme().Muted, fmt.Sprintf(" (+%d file%s)", remaining, plural(remaining)))
	}
	return header
}

// FormatGrepHeader returns the styled grep call: bold toolTitle `grep`, accent
// `/pattern/`, toolOutput ` in <path>` ($HOME shortened, "." by default),
// then optional ` (glob)` and ` limit N` suffixes. Non-string pattern or
// path arguments render as the invalid-arg marker.
func FormatGrepHeader(raw json.RawMessage) string {
	args := decodeToolArgs(raw)
	theme := ActiveTheme()
	header := toolTitleText("grep") + " " + listPatternText(args["pattern"], true) +
		fg(theme.ToolOutput, " in "+listPathText(args["path"]))
	if glob, ok := renderStr(args["glob"]); ok && glob != "" {
		header += fg(theme.ToolOutput, " ("+glob+")")
	}
	if limit := args["limit"]; limit != nil {
		header += fg(theme.ToolOutput, " limit "+fmt.Sprint(limit))
	}
	return header
}

// FormatFindHeader returns the styled find call.
func FormatFindHeader(raw json.RawMessage) string {
	args := decodeToolArgs(raw)
	theme := ActiveTheme()
	header := toolTitleText("find") + " " + listPatternText(args["pattern"], false) +
		fg(theme.ToolOutput, " in "+listPathText(args["path"]))
	if limit := args["limit"]; limit != nil {
		header += fg(theme.ToolOutput, " (limit "+fmt.Sprint(limit)+")")
	}
	return header
}

// FormatLsHeader returns the styled ls call: renderToolPath with "." for an
// empty path.
func FormatLsHeader(raw json.RawMessage, cwd string) string {
	args := decodeToolArgs(raw)
	header := toolTitleText("ls") + " "
	switch path, ok := renderStr(args["path"]); {
	case !ok:
		header += invalidArgText()
	case path == "":
		header += renderToolPath(".", cwd)
	default:
		header += renderToolPath(path, cwd)
	}
	if limit := args["limit"]; limit != nil {
		header += fg(ActiveTheme().ToolOutput, " (limit "+fmt.Sprint(limit)+")")
	}
	return header
}

// decodeToolArgs decodes a tool call's arguments:
// a JSON object, or nothing while the arguments are still streaming.
func decodeToolArgs(raw json.RawMessage) map[string]any {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	return args
}

// listPatternText renders grep's accent `/pattern/` or find's accent
// `pattern`, or the invalid-arg marker for a non-string pattern.
func listPatternText(v any, slashes bool) string {
	pattern, ok := renderStr(v)
	if !ok {
		return invalidArgText()
	}
	if slashes {
		pattern = "/" + pattern + "/"
	}
	return fg(ActiveTheme().Accent, pattern)
}

// listPathText renders grep/find's path with $HOME shortened ("." when
// empty), or the invalid-arg marker for a non-string path.
func listPathText(v any) string {
	path, ok := renderStr(v)
	if !ok {
		return invalidArgText()
	}
	if path == "" {
		path = "."
	}
	return shortenPath(path)
}

// FormatBuiltinToolHeader dispatches to the per-tool header formatter.
// cwd resolves relative paths
// to absolute file:// URLs for the OSC-8 hyperlink. Returns "" if the
// tool has no custom header format (falls back to FormatToolArgs).
func FormatBuiltinToolHeader(toolName string, args json.RawMessage, cwd string) string {
	switch toolName {
	case "bash", "powershell":
		prompt, _ := ShellToolPrompt(toolName)
		return FormatShellHeader(args, prompt)
	case "read":
		return FormatReadHeader(args, cwd)
	case "write":
		return FormatWriteHeader(args, cwd)
	case "edit":
		return FormatEditHeader(args, cwd)
	case "grep":
		return FormatGrepHeader(args)
	case "find":
		return FormatFindHeader(args)
	case "ls":
		return FormatLsHeader(args, cwd)
	}
	return ""
}

// HeaderForTool returns the fully styled call header for any tool: the
// builtin per-tool formatter when one matches, otherwise the default of a
// bold toolTitle tool name followed by its compact args. The returned string is
// self-styled; renderHeaderInner emits it verbatim.
func HeaderForTool(name string, args json.RawMessage, cwd string) string {
	if h := FormatBuiltinToolHeader(name, args, cwd); h != "" {
		return h
	}
	if a := FormatToolArgs(args); a != "" {
		return toolTitleText(name) + " " + a
	}
	return toolTitleText(name)
}

func truncateArg(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "\u2026"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// stripControlEscapes removes terminal control sequences from `s` while
// preserving SGR color codes. Used to sanitize tool output before
// rendering it inside the TUI: a bash script that does `clear` or
// `printf '\x1b[H'` would otherwise blow away our display.
//
// Kept:
//   - SGR sequences (`ESC [ ... m`) for `ls --color`, ripgrep, etc.
//   - Plain text, tabs, newlines, regular carriage returns.
//
// Dropped:
//   - Cursor positioning (`H`, `f`, `A`, `B`, `C`, `D`, `G`, `s`, `u`)
//   - Erase commands (`J`, `K`)
//   - Mode set/reset (`?...h`, `?...l`): hide cursor, alt screen, etc.
//   - OSC sequences (`ESC ]` … BEL or ST)
//   - Lone ESC, BEL, and other C0 control bytes (except \t \n \r).
func stripControlEscapes(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[':
				// CSI sequence: scan for final byte in 0x40–0x7E.
				j := i + 2
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) {
					if s[j] == 'm' {
						// Keep SGR (color) sequences.
						out.WriteString(s[i : j+1])
					}
					i = j + 1
					continue
				}
				// Unterminated CSI: drop the rest defensively.
				return out.String()
			case ']':
				// OSC sequence: terminated by BEL (0x07) or ST (ESC \).
				j := i + 2
				for j < len(s) {
					if s[j] == 0x07 {
						j++
						break
					}
					if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			default:
				// Two-byte ESC sequence (e.g. ESC = / ESC > / ESC c). Drop both.
				i += 2
				continue
			}
		}
		if c == 0x1b {
			// Lone ESC at end of string: drop.
			i++
			continue
		}
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			// Other C0 control bytes (BEL, BS, FF, ...): drop.
			i++
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}
