package codingagent

// tool_render_shell.go: result-side presentation for the built-in shell tools.
//
// The body renderer is shared by bash and powershell. The call header and duration formatting live in
// tui/shell_renderers.go.

import (
	"cmp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// bashPreviewLines is the collapsed shell output preview height.
const bashPreviewLines = 5

// shellDiffRows caps the diff rows a collapsed card shows per changed file.
const shellDiffRows = 12

// shellResultDetails is the part of BashToolDetails the renderer reads.
type shellResultDetails struct {
	Truncation     *tools.TruncationResult
	FullOutputPath string
	Changes        []tools.ShellFileChange
}

// shellDetailsFrom reads BashToolDetails from a live result (*tools.BashDetails)
// or from a persisted or extension-supplied result, whose details arrive as the
// decoded JSON shape ({truncation, fullOutputPath}).
func shellDetailsFrom(details any) shellResultDetails {
	switch d := details.(type) {
	case *tools.BashDetails:
		if d == nil {
			return shellResultDetails{}
		}
		return shellResultDetails{Truncation: d.Truncation, FullOutputPath: d.FullOutputPath, Changes: d.Changes}
	case map[string]any:
		out := shellResultDetails{}
		out.FullOutputPath, _ = d["fullOutputPath"].(string)
		if raw, ok := d["changes"].([]any); ok {
			for _, item := range raw {
				if c, ok := decodeAs[tools.ShellFileChange](item); ok {
					// Sessions saved before binary files were skipped can
					// hold megabytes of diff; never draw those.
					if len(c.Diff) > tools.MaxShellDiffBytes {
						c.Diff = ""
					}
					out.Changes = append(out.Changes, c)
				}
			}
		}
		if raw, ok := d["truncation"].(map[string]any); ok {
			type truncationWire struct {
				Truncated   bool   `json:"truncated"`
				TruncatedBy string `json:"truncatedBy"`
				TotalLines  int    `json:"totalLines"`
				OutputLines int    `json:"outputLines"`
				MaxBytes    int    `json:"maxBytes"`
			}
			if wire, ok := decodeAs[truncationWire](raw); ok {
				out.Truncation = &tools.TruncationResult{
					Truncated:   wire.Truncated,
					TruncatedBy: wire.TruncatedBy,
					TotalLines:  wire.TotalLines,
					OutputLines: wire.OutputLines,
					MaxBytes:    wire.MaxBytes,
				}
			}
		}
		return out
	}
	return shellResultDetails{}
}

// makeShellBodyRenderer produces the body of a bash or powershell tool card.
// It shows the trimmed output (minus the full-output footer the tool appends
// once it finishes), in full when expanded or as the last BASH_PREVIEW_LINES visual lines behind an
// "earlier lines" hint when collapsed; then the truncation warning; then the
// "Took" footer for a finished run that recorded its start (took non-nil, even
// when the clock measured 0).
// isPartial marks a live update: the component draws the running "Elapsed"
// footer itself.
//
// The body's sections each begin with a blank line; the tool card draws the
// first one as its separator row, so the returned lines omit it.
func makeShellBodyRenderer(content string, details any, isPartial bool, took *time.Duration) func(width int, expanded bool) []string {
	d := shellDetailsFrom(details)
	return func(width int, expanded bool) []string {
		lines := shellResultLines(content, d, isPartial, expanded, width)
		if !isPartial && took != nil {
			footer := themeFg(tui.ActiveTheme().Muted, "Took "+tui.FormatToolDuration(*took))
			lines = append(lines, tui.NewPaddedText("\n"+footer, 0, 0, nil).Render(width)...)
		}
		if len(lines) > 0 {
			lines = lines[1:]
		}
		return lines
	}
}

func shellResultLines(content string, d shellResultDetails, isPartial, expanded bool, width int) []string {
	theme := tui.ActiveTheme()
	output := jsTrim(shellTextOutput(content))
	truncated := d.Truncation != nil && d.Truncation.Truncated
	if !isPartial && truncated && d.FullOutputPath != "" && strings.HasSuffix(output, "]") {
		if footerStart := strings.LastIndex(output, "\n\n["); footerStart != -1 && strings.Contains(output[footerStart:], d.FullOutputPath) {
			output = jsTrimEnd(output[:footerStart])
		}
	}

	var lines []string
	if output != "" {
		styled := strings.Split(output, "\n")
		for i, line := range styled {
			styled[i] = themeFg(theme.ToolOutput, line)
		}
		styledOutput := strings.Join(styled, "\n")
		if expanded {
			lines = append(lines, tui.NewPaddedText("\n"+styledOutput, 0, 0, nil).Render(width)...)
		} else {
			preview := tui.TruncateToVisualLines(styledOutput, bashPreviewLines, width)
			lines = append(lines, "")
			if preview.SkippedCount > 0 {
				hint := themeFg(theme.Muted, "... ("+strconv.Itoa(preview.SkippedCount)+" earlier lines,") +
					" " + expandKeyHint() + themeFg(theme.Muted, ")")
				lines = append(lines, widthx.TruncateToWidth(hint, width, "...", false))
			}
			lines = append(lines, preview.VisualLines...)
		}
	}

	lines = append(lines, shellChangeLines(d.Changes, expanded, width)...)

	if truncated || d.FullOutputPath != "" {
		var warnings []string
		if d.FullOutputPath != "" {
			warnings = append(warnings, "Full output: "+d.FullOutputPath)
		}
		if truncated {
			tr := d.Truncation
			if tr.TruncatedBy == "lines" {
				warnings = append(warnings, "Truncated: showing "+strconv.Itoa(tr.OutputLines)+" of "+strconv.Itoa(tr.TotalLines)+" lines")
			} else {
				maxBytes := cmp.Or(tr.MaxBytes, tools.DefaultMaxBytes)
				warnings = append(warnings, "Truncated: "+strconv.Itoa(tr.OutputLines)+" lines shown ("+tools.FormatSize(maxBytes)+" limit)")
			}
		}
		warning := themeFg(theme.Warning, "["+strings.Join(warnings, ". ")+"]")
		lines = append(lines, tui.NewPaddedText("\n"+warning, 0, 0, nil).Render(width)...)
	}
	return lines
}

// shellChangesRenderer renders the files a shell result's command changed,
// or is nil when it changed none.
func shellChangesRenderer(details any) func(width int, expanded bool) []string {
	changes := shellDetailsFrom(details).Changes
	if len(changes) == 0 {
		return nil
	}
	return func(width int, expanded bool) []string { return shellChangeLines(changes, expanded, width) }
}

// shellChangeLines shows the files a command changed, each with its diff:
// shellDiffRows rows per file while collapsed, all of them expanded.
func shellChangeLines(changes []tools.ShellFileChange, expanded bool, width int) []string {
	theme := tui.ActiveTheme()
	var lines []string
	for _, c := range changes {
		lines = append(lines, "", widthx.TruncateToWidth(themeFg(theme.Muted, "± "+c.Kind+" ")+c.Path, width, "…", false))
		rows := tui.RenderDiffRows(c.Diff, width)
		if !expanded && len(rows) > shellDiffRows {
			more := themeFg(theme.Muted, "... ("+strconv.Itoa(len(rows)-shellDiffRows)+" more lines,") + " " + expandKeyHint() + themeFg(theme.Muted, ")")
			rows = append(rows[:shellDiffRows:shellDiffRows], widthx.TruncateToWidth(more, width, "...", false))
		}
		lines = append(lines, rows...)
	}
	return lines
}

// shellTextOutput normalizes a text result: strip ANSI, sanitize binary output, and drop carriage returns.
func shellTextOutput(content string) string {
	text := tools.SanitizeBinaryOutput(string(tools.StripANSI([]byte(content))))
	return strings.ReplaceAll(text, "\r", "")
}

// expandKeyHint renders the "<key> to expand" hint.
func expandKeyHint() string {
	theme := tui.ActiveTheme()
	return themeFg(theme.Dim, tui.AppKeyText("app.tools.expand", "ctrl+o")) + themeFg(theme.Muted, " to expand")
}

// themeFg paints text: color, text, then the foreground reset.
func themeFg(color, text string) string {
	if color == "" {
		return text
	}
	return color + text + tui.SGRFgReset
}

// isJSWhitespace reports whether r is JavaScript WhiteSpace or LineTerminator,
// the set String.prototype.trim removes. It differs from unicode.IsSpace in
// U+FEFF (JS whitespace) and U+0085 (not JS whitespace).
func isJSWhitespace(r rune) bool {
	switch r {
	case '\uFEFF':
		return true
	case '\u0085':
		return false
	}
	return unicode.IsSpace(r)
}

func jsTrim(s string) string    { return strings.TrimFunc(s, isJSWhitespace) }
func jsTrimEnd(s string) string { return strings.TrimRightFunc(s, isJSWhitespace) }
