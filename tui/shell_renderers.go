package tui

// shell_renderers.go: call-side presentation for the built-in shell tools.
//
// Bash and powershell share one renderer and differ only in their prompt
// (ShellToolPrompt). The result half (preview, truncation warnings, "Took"
// footer) lives in internal/codingagent/tool_render_shell.go.

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// FormatShellHeader renders a shell tool call: the whole `<prompt> <command>`
// is toolTitle-colored and bold, followed by a muted ` (timeout Ns)` suffix
// when the timeout argument is truthy. A non-string command renders as the
// error-colored invalid-arg marker; an empty or missing command renders as a
// toolOutput "...".
func FormatShellHeader(raw json.RawMessage, prompt string) string {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	theme := ActiveTheme()
	command, valid := renderStr(args["command"])
	var commandDisplay string
	switch {
	case !valid:
		commandDisplay = invalidArgText()
	case command != "":
		commandDisplay = command
	default:
		commandDisplay = fg(theme.ToolOutput, "...")
	}
	timeoutSuffix := ""
	if timeout := args["timeout"]; timeout != nil && timeout != 0.0 && timeout != "" && timeout != false {
		timeoutSuffix = fg(theme.Muted, " (timeout "+fmt.Sprint(timeout)+"s)")
	}
	return toolTitleText(prompt+" "+commandDisplay) + timeoutSuffix
}

// renderStr converts a JSON-decoded argument: a string passes through, null
// becomes "", and any other value is invalid.
func renderStr(v any) (string, bool) {
	switch s := v.(type) {
	case nil:
		return "", true
	case string:
		return s, true
	}
	return "", false
}

// invalidArgText is the error-colored marker for an invalid argument.
func invalidArgText() string {
	return fg(ActiveTheme().Error, "[invalid arg]")
}

// FormatToolDuration formats a whole-millisecond duration: seconds with one
// decimal under a minute, then "Xm Ys", then "Xh Ym Zs".
func FormatToolDuration(d time.Duration) string {
	ms := d.Milliseconds()
	seconds := float64(ms) / 1000
	if seconds < 60 {
		return strconv.FormatFloat(seconds, 'f', 1, 64) + "s"
	}
	totalSeconds := int64(math.Floor(seconds))
	minutes := totalSeconds / 60
	remainder := totalSeconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, remainder)
	}
	return fmt.Sprintf("%dh %dm %ds", minutes/60, minutes%60, remainder)
}
