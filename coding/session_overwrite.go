package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Overwrite guard: a write that would replace a file the session found
// already there, hasn't changed, and shares almost nothing with is refused
// to the model, which then picks a new path or passes overwrite: true. A
// file changed since the session started (by the session, a shell command,
// or the user) is the session's to rewrite.

const (
	// overwriteMinLines skips small files, where a full rewrite is normal.
	overwriteMinLines = 3
	// overwriteMaxShared is the share of the existing file's lines below
	// which a write counts as replacing it with something else.
	overwriteMaxShared = 0.2
)

// initOverwriteGuard installs the guard before write calls.
func (s *Session) initOverwriteGuard() {
	s.agent.AddBeforeToolCallHook(func(_ context.Context, _, toolName string, args json.RawMessage) agent.ToolCallHookResult {
		if toolName != "write" {
			return agent.ToolCallHookResult{}
		}
		if reason := overwriteCheck(s.services.CWD(), args, s.startedAt()); reason != "" {
			return agent.ToolCallHookResult{Block: true, Reason: reason}
		}
		return agent.ToolCallHookResult{}
	})
}

// overwriteCheck returns why a write call with args should be refused, or "".
func overwriteCheck(cwd string, args json.RawMessage, started time.Time) string {
	var in struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Overwrite bool   `json:"overwrite"`
	}
	if json.Unmarshal(args, &in) != nil || in.Path == "" || in.Overwrite {
		return ""
	}
	return overwriteRefusal(tools.ResolvePath(cwd, in.Path), in.Path, in.Content, started)
}

// startedAt is when the session began: its header's time, or now for a
// session without one.
func (s *Session) startedAt() time.Time {
	if s.inner != nil {
		if t, err := time.Parse(time.RFC3339Nano, s.inner.Header().Timestamp); err == nil {
			return t
		}
	}
	return time.Now()
}

// overwriteRefusal returns why writing content to path would replace an
// unrelated file, or "".
func overwriteRefusal(path, shown, content string, started time.Time) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.ModTime().After(started) {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	old := substantiveLines(string(data))
	if len(old) < overwriteMinLines {
		return ""
	}
	kept := map[string]bool{}
	for _, line := range substantiveLines(content) {
		kept[line] = true
	}
	shared := 0
	for _, line := range old {
		if kept[line] {
			shared++
		}
	}
	if float64(shared)/float64(len(old)) >= overwriteMaxShared {
		return ""
	}
	return fmt.Sprintf("%s already holds something else (%q) that this session hasn't changed. Write to a new path, or pass overwrite: true to replace it.", shown, fileTitle(old))
}

// boilerplate matches lines two unrelated files share anyway: bare markup
// tags and lone brackets or punctuation.
var boilerplate = regexp.MustCompile(`^((</?[A-Za-z!][^>]*>)+|[\s{}()\[\];,]*)$`)

// substantiveLines returns the trimmed lines that say something about what
// the file is.
func substantiveLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !boilerplate.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

var htmlTitle = regexp.MustCompile(`(?i)<title>\s*(.*?)\s*</title>`)

// fileTitle names a file by its title, first heading, or first line.
func fileTitle(lines []string) string {
	for _, line := range lines {
		if m := htmlTitle.FindStringSubmatch(line); m != nil && m[1] != "" {
			return truncateRunes(m[1], 60)
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			return truncateRunes(strings.TrimSpace(strings.TrimLeft(line, "#")), 60)
		}
	}
	return truncateRunes(lines[0], 60)
}
