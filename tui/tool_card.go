package tui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/tui/widthx"
)

// Tool cards come in two forms. An inline card is one indented line: an icon
// and a short description ("→ Read main.go"), muted once done. A block card is
// a panel with a heavy left notch, a muted title, and a body (command output,
// a diff, file content). Consecutive inline cards stack without blank lines;
// anything else is separated by one.

const (
	toolIndent        = 3  // columns before an inline card's icon
	toolBlockPadX     = 2  // columns between a block card's notch and its text
	toolShellPreview  = 10 // output lines a collapsed shell block shows
	toolWritePreview  = 10 // content lines a collapsed write block shows
	toolErrorPreview  = 3  // error lines an inline card shows
	toolSpinnerPeriod = 80 * time.Millisecond
)

var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SetArgs records the call's raw JSON arguments, from which cards build their
// titles. Invalid or partial JSON is ignored.
func (c *ToolExecutionComponent) SetArgs(args json.RawMessage) {
	if len(args) == 0 || !json.Valid(args) {
		return
	}
	c.args = append(c.args[:0], args...)
	c.Invalidate()
}

// SetPrevious records the transcript item rendered just above this card, which
// decides whether the card needs a blank separator line.
func (c *ToolExecutionComponent) SetPrevious(previous Component) {
	c.previous = previous
	c.Invalidate()
}

// IsInline reports whether the card currently renders as a one-line card.
func (c *ToolExecutionComponent) IsInline() bool {
	return !c.blockForm()
}

func (c *ToolExecutionComponent) separated() bool {
	switch prev := c.previous.(type) {
	case nil:
		return true
	case *Spacer:
		return false
	case *ToolExecutionComponent:
		return !prev.IsInline() || !c.IsInline()
	}
	return true
}

// Arg returns a call argument as display text: a string as is, any other
// scalar formatted, "" when absent.
func (c *ToolExecutionComponent) Arg(key string) string { return c.arg(key) }

func (c *ToolExecutionComponent) arg(key string) string {
	if len(c.args) == 0 {
		return ""
	}
	value, ok := decodeToolArgs(c.args)[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return formatArgValue(value)
}

// toolPath formats a path argument relative to the working directory, or
// with a "~" home prefix outside it.
func (c *ToolExecutionComponent) toolPath(keys ...string) string {
	var path string
	for _, key := range keys {
		if path = c.arg(key); path != "" {
			break
		}
	}
	return c.displayPath(path)
}

// displayPath shows path relative to the working directory when inside it,
// else with ~ for the home directory.
func (c *ToolExecutionComponent) displayPath(path string) string {
	if path == "" {
		return ""
	}
	if c.Cwd != "" {
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(c.Cwd, abs)
		}
		if rel, err := filepath.Rel(c.Cwd, abs); err == nil {
			if rel == "." {
				return "."
			}
			if !strings.HasPrefix(rel, "..") {
				return rel
			}
		}
		path = abs
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// extraArgs renders the scalar arguments other than omit as "[k=v, k2=v2]".
func (c *ToolExecutionComponent) extraArgs(omit ...string) string {
	if len(c.args) == 0 {
		return ""
	}
	decoded := decodeToolArgs(c.args)
	keys := make([]string, 0, len(decoded))
	for key, value := range decoded {
		switch value.(type) {
		case string, float64, bool, json.Number:
			if !slices.Contains(omit, key) {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		return ""
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key + "=" + c.arg(key)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func joinNonEmpty(parts ...string) string {
	kept := parts[:0:0]
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

// outputCount is the number of non-empty output lines, or 0 for an error or
// a "No ..." result.
func (c *ToolExecutionComponent) outputCount() int {
	if c.State != ToolStateDone || c.Output == "" || strings.HasPrefix(strings.TrimSpace(c.Output), "No ") {
		return 0
	}
	n := 0
	for line := range strings.SplitSeq(strings.TrimSpace(c.Output), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func countLabel(n int, one, many string) string {
	if n <= 0 {
		return ""
	}
	if n == 1 {
		return fmt.Sprintf("(1 %s)", one)
	}
	return fmt.Sprintf("(%d %s)", n, many)
}

// inlineSpec is an inline card's icon, description, and the text shown while
// its arguments are still streaming.
func (c *ToolExecutionComponent) inlineSpec() (icon, text, pending string) {
	name := c.Name
	switch {
	case IsShellTool(name):
		return strings.TrimSpace(c.shellPrompt()), c.arg("command"), "Writing command…"
	case name == "read":
		return "→", joinNonEmpty("Read", c.toolPath("path", "file_path", "filePath"), c.extraArgs("path", "file_path", "filePath")), "Reading file…"
	case name == "write":
		return "←", joinNonEmpty("Write", c.toolPath("path", "file_path", "filePath")), "Preparing write…"
	case name == "edit":
		return "←", joinNonEmpty("Edit", c.toolPath("path", "file_path", "filePath")), "Preparing edit…"
	case name == "grep":
		text := fmt.Sprintf("Grep %q", c.arg("pattern"))
		if path := c.toolPath("path"); path != "" {
			text += " in " + path
		}
		return "✱", joinNonEmpty(text, countLabel(c.outputCount(), "match", "matches")), "Searching content…"
	case name == "find" || name == "glob":
		text := fmt.Sprintf("Glob %q", c.arg("pattern"))
		if path := c.toolPath("path"); path != "" {
			text += " in " + path
		}
		return "✱", joinNonEmpty(text, countLabel(c.outputCount(), "match", "matches")), "Finding files…"
	case name == "ls":
		path := cmp.Or(c.toolPath("path"), ".")
		return "→", "List " + path, "Listing directory…"
	case name == "webfetch" || name == "web_fetch":
		return "%", joinNonEmpty("WebFetch", c.arg("url")), "Fetching from the web…"
	case name == "task":
		kind := cmp.Or(c.arg("type"), "task")
		return "│", joinNonEmpty(strings.ToUpper(kind[:1])+kind[1:], "—", c.arg("description")), "Delegating…"
	case name == "websearch" || name == "web_search":
		return "◈", fmt.Sprintf("Web Search %q", c.arg("query")), "Searching web…"
	case name == "council":
		// A finished call's first output line is its title ("War council
		// built: 3 candidates (2 passing)").
		if first, _, _ := strings.Cut(strings.TrimSpace(c.Output), "\n"); c.State == ToolStateDone && strings.HasPrefix(first, "War council built") {
			return "☢", first, ""
		}
		switch c.arg("action") {
		case "build":
			return "☢", "War council building", "War council building…"
		case "apply":
			return "☢", "Apply council candidate " + c.arg("candidate"), "Applying candidate…"
		}
		return "☢", joinNonEmpty("War council", c.arg("question")), "Asking the war council…"
	}
	label := c.Name
	if c.Label != "" {
		label = c.Label
	}
	return "⚙", joinNonEmpty(label, c.extraArgs()), "Preparing " + label + "…"
}

// blockForm reports whether the card renders as a panel: shell calls with
// output, finished edits and writes with a body, and any card the user
// expanded that has something to show.
func (c *ToolExecutionComponent) blockForm() bool {
	switch {
	case IsShellTool(c.Name):
		return c.Output != "" || (c.State == ToolStateRunning && c.executionStarted)
	case c.Name == "edit":
		return c.State == ToolStateDone && c.BodyRenderer != nil
	case c.Name == "write":
		return c.State == ToolStateDone && c.arg("content") != ""
	}
	return !c.Collapsed && c.State != ToolStateRunning && (c.Output != "" || c.BodyRenderer != nil)
}

// streamingText is the part of a call worth showing while its arguments
// stream: the command for shell calls, the description for inline text
// that already names a path or pattern, else "".
func (c *ToolExecutionComponent) streamingText(text string) string {
	if IsShellTool(c.Name) {
		return c.arg("command")
	}
	if c.arg("path") != "" || c.arg("file_path") != "" || c.arg("filePath") != "" || c.arg("pattern") != "" || c.arg("url") != "" || c.arg("description") != "" || c.arg("query") != "" {
		return text
	}
	// The args don't parse yet: a long write shows its path as soon as it
	// arrives and how many lines have come in.
	if c.streamPath == "" && c.streamLines == 0 {
		return ""
	}
	shown := joinNonEmpty(toolTitleWord(c.Name), c.displayPath(c.streamPath))
	if c.streamLines > 0 {
		shown += fmt.Sprintf(" · %d line%s", c.streamLines, plural(c.streamLines))
	}
	return shown
}

// toolTitleWord is a tool's name as a capitalized word: write → Write.
func toolTitleWord(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func (c *ToolExecutionComponent) spinnerFrame() string {
	// A call still streaming its arguments has no start time yet; animate
	// from the wall clock so its spinner moves too.
	elapsed := time.Duration(time.Now().UnixNano())
	if !c.StartedAt.IsZero() {
		elapsed = time.Since(c.StartedAt)
	}
	return brailleFrames[int(elapsed/toolSpinnerPeriod)%len(brailleFrames)]
}

func (c *ToolExecutionComponent) renderCard(width int) []string {
	var out []string
	if c.separated() {
		out = append(out, "")
	}
	if c.blockForm() {
		return append(out, c.renderBlockCard(width)...)
	}
	return append(out, c.renderInlineCard(width)...)
}

func (c *ToolExecutionComponent) renderInlineCard(width int) []string {
	lines := c.renderInlineHead(width)
	if c.Subline == "" || (c.State == ToolStateRunning && !c.executionStarted) {
		return lines
	}
	token := "textMuted"
	if c.SubWarn || c.State == ToolStateError {
		token = "warning"
	}
	sub := widthx.TruncateToWidth("↳ "+c.Subline, max(1, width-toolIndent-2), "…", false)
	return append(lines, strings.Repeat(" ", toolIndent+2)+ActiveTheme().FgText(token, sub))
}

func (c *ToolExecutionComponent) renderInlineHead(width int) []string {
	th := ActiveTheme()
	indent := strings.Repeat(" ", toolIndent)
	icon, text, pending := c.inlineSpec()
	textWidth := max(1, width-toolIndent-2)
	switch {
	case c.State == ToolStateRunning && !c.executionStarted:
		// Arguments are still streaming: show what has arrived so far, or
		// what the call is getting ready, muted beside the spinner.
		shown := pending
		if partial := strings.TrimSpace(c.streamingText(text)); partial != "" {
			shown = partial + "…"
		}
		line := widthx.TruncateToWidth(strings.ReplaceAll(shown, "\n", " "), textWidth, "…", false)
		return []string{indent + th.FgText("accent", c.spinnerFrame()) + " " + th.FgText("textMuted", line)}
	case c.State == ToolStateRunning:
		lines := wrapText(text, textWidth)
		out := []string{indent + th.FgText("accent", c.spinnerFrame()) + " " + th.FgText("text", lines[0])}
		for _, line := range lines[1:] {
			out = append(out, indent+"  "+th.FgText("text", line))
		}
		return out
	}
	token := "textMuted"
	iconToken := "textMuted"
	if c.State == ToolStateError {
		token, iconToken = "error", "error"
	}
	lines := wrapText(text, textWidth)
	out := []string{indent + th.FgText(iconToken, icon) + " " + th.FgText(token, lines[0])}
	for _, line := range lines[1:] {
		out = append(out, indent+"  "+th.FgText(token, line))
	}
	if c.State == ToolStateError {
		for _, line := range errorPreviewLines(stripControlEscapes(c.Output), toolErrorPreview, width-toolIndent-2) {
			out = append(out, indent+"  "+th.FgText("error", line))
		}
	}
	return out
}

// errorPreviewLines returns at most n wrapped, ANSI-free lines of text, ending in
// "…" when more remain.
func errorPreviewLines(text string, n, width int) []string {
	text = strings.TrimSpace(widthx.StripAnsi(strings.ReplaceAll(text, "\t", "   ")))
	if text == "" {
		return nil
	}
	var lines []string
	for line := range strings.SplitSeq(text, "\n") {
		lines = append(lines, wrapText(line, max(1, width))...)
	}
	if len(lines) > n {
		lines = append(lines[:n:n], "…")
	}
	return lines
}

func (c *ToolExecutionComponent) renderBlockCard(width int) []string {
	th := ActiveTheme()
	bodyWidth := max(1, width-1-toolBlockPadX-1)
	var title string
	var body []string
	icon, text, _ := c.inlineSpec()
	expanded := !c.Collapsed
	switch {
	case IsShellTool(c.Name):
		if dir := c.toolPath("cwd", "workdir"); dir != "" && dir != "." {
			title = "# Running in " + dir
		}
		prefix := c.shellPrompt()
		if c.State == ToolStateRunning {
			prefix = c.spinnerFrame() + " "
		}
		for i, line := range wrapAll(c.arg("command"), max(1, bodyWidth-2)) {
			lead := "  "
			if i == 0 {
				lead = prefix
			}
			body = append(body, th.FgText("text", lead+line))
		}
		output := strings.TrimSpace(widthx.StripAnsi(stripControlEscapes(c.Output)))
		if output != "" {
			body = append(body, "")
			lines := wrapAll(output, bodyWidth)
			if !expanded && len(lines) > toolShellPreview {
				hidden := len(lines) - toolShellPreview
				lines = append(lines[:toolShellPreview:toolShellPreview], "…")
				body = append(body, colorLines(th, "text", lines)...)
				body = append(body, "", th.FgText("textMuted", fmt.Sprintf("%d more lines · ctrl+o to expand", hidden)))
			} else {
				body = append(body, colorLines(th, "text", lines)...)
			}
		}
		if c.ShellChanges != nil {
			body = append(body, c.ShellChanges(bodyWidth, expanded)...)
		}
	case c.Name == "edit":
		title = icon + " " + text
		body = flattenVisualRows(c.BodyRenderer(bodyWidth, true))
	case c.Name == "write":
		title = "# Wrote " + c.toolPath("path", "file_path", "filePath")
		body = c.numberedContent(c.arg("content"), bodyWidth, expanded)
	default:
		title = "# " + text
		// An error's output is appended below in the error color, so the
		// plain output is shown only for a successful result.
		var output []string
		if c.State != ToolStateError {
			output = colorLines(th, "text", wrapAll(strings.TrimSpace(widthx.StripAnsi(stripControlEscapes(c.Output))), bodyWidth))
		}
		switch {
		case c.BodyRenderer != nil:
			body = flattenVisualRows(c.BodyRenderer(bodyWidth, true))
		default:
			body = output
		}
	}
	if c.State == ToolStateError && !IsShellTool(c.Name) {
		body = append(body, colorLines(th, "error", wrapAll(strings.TrimSpace(widthx.StripAnsi(c.Output)), bodyWidth))...)
	}

	panel := th.Bg("backgroundPanel")
	notch := th.Fg("background") + "┃" + SGRFgReset
	pad := strings.Repeat(" ", toolBlockPadX)
	row := func(content string) string {
		return FillBackground(notch+pad+content, width, panel)
	}
	out := []string{row("")}
	if title != "" {
		for _, line := range wrapText(title, bodyWidth) {
			out = append(out, row(th.FgText("textMuted", line)))
		}
		if len(body) > 0 {
			out = append(out, row(""))
		}
	}
	for _, line := range body {
		out = append(out, row(line))
	}
	return append(out, row(""))
}

// numberedContent renders file content with a muted line-number gutter,
// collapsed to the first lines unless expanded.
func (c *ToolExecutionComponent) numberedContent(content string, width int, expanded bool) []string {
	th := ActiveTheme()
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	hidden := 0
	if !expanded && len(lines) > toolWritePreview {
		hidden = len(lines) - toolWritePreview
		lines = lines[:toolWritePreview]
	}
	gutter := max(3, len(fmt.Sprint(len(lines)+hidden)))
	var out []string
	for i, line := range lines {
		number := fmt.Sprintf("%*d ", gutter, i+1)
		wrapped := wrapText(strings.ReplaceAll(line, "\t", "   "), max(1, width-gutter-1))
		for j, part := range wrapped {
			if j > 0 {
				number = strings.Repeat(" ", gutter+1)
			}
			out = append(out, th.FgText("textMuted", number)+th.FgText("text", part))
		}
	}
	if hidden > 0 {
		out = append(out, "", th.FgText("textMuted", fmt.Sprintf("%d more lines · ctrl+o to expand", hidden)))
	}
	return out
}

func wrapAll(text string, width int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\t", "   "), "\n") {
		out = append(out, wrapText(line, max(1, width))...)
	}
	return out
}

func colorLines(th *Theme, token string, lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = th.FgText(token, line)
	}
	return out
}

// shellPrompt is the prompt shown before a shell command: "PS> " for
// PowerShell, "$ " otherwise.
func (c *ToolExecutionComponent) shellPrompt() string {
	if c.Name == "powershell" {
		return "PS> "
	}
	return "$ "
}
