package coding

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// Stall detection: a small model that repeats the same tool call, or reads
// for many turns without changing a file, is told once to change approach.

const (
	// stallRepeats is how many identical tool calls count as a loop.
	stallRepeats = 3
	// stallIdleTurns is how many turns without progress count as idle, and
	// stallIdleTime how long they must also have taken, so a burst of fast
	// turns alone never nudges.
	stallIdleTurns = 10
	stallIdleTime  = 3 * time.Minute
	// stallGapCap is the most one idle turn adds to the idle time.
	stallGapCap = time.Minute
	// stallMaxNudges bounds the nudges in one run.
	stallMaxNudges = 3
	// stallMessageType is the custom message type of a nudge.
	stallMessageType = "stall_nudge"
)

// researchTools are the tools whose call with a query, URL, or file not
// seen before in the run counts as progress: the model found new
// information.
var researchTools = map[string]string{"web_search": "query", "web_fetch": "url", "read": "path"}

// fileChangingTools are the tools whose success counts as progress.
var fileChangingTools = map[string]bool{"edit": true, "write": true, "apply_patch": true}

// fileWatch spots file changes made through the shell: it keeps the
// modification time of every file the model read or wrote, and after each
// shell command marks the call as a change when any of them moved. A file
// the model never touched isn't watched.
type fileWatch struct {
	mu      sync.Mutex
	mtimes  map[string]time.Time
	changed map[string]bool // tool call IDs whose command changed a watched file
}

// afterToolCall records the files read, written, or edited, and checks the
// watched files after a shell command.
func (w *fileWatch) afterToolCall(cwd string) agent.AfterToolCallHook {
	return func(_ context.Context, toolCallID, toolName string, args json.RawMessage, _ agent.AgentToolResult) agent.AfterToolCallResult {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.mtimes == nil {
			w.mtimes, w.changed = map[string]time.Time{}, map[string]bool{}
		}
		switch toolName {
		case "read", "write", "edit":
			var in struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(args, &in) == nil && in.Path != "" {
				path := in.Path
				if !filepath.IsAbs(path) {
					path = filepath.Join(cwd, path)
				}
				if info, err := os.Stat(path); err == nil {
					w.mtimes[path] = info.ModTime()
				}
			}
		case "bash":
			for path, before := range w.mtimes {
				info, err := os.Stat(path)
				if err != nil || info.ModTime().Equal(before) {
					continue
				}
				w.mtimes[path] = info.ModTime()
				w.changed[toolCallID] = true
			}
		}
		return agent.AfterToolCallResult{}
	}
}

// paths lists the files the model read or wrote, sorted.
func (w *fileWatch) paths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(maps.Keys(w.mtimes))
}

// changedFile reports whether a shell call changed a watched file.
func (w *fileWatch) changedFile(toolCallID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changed[toolCallID]
}

// stallState is one run's nudge bookkeeping, reset with each prompt.
type stallState struct {
	nudged map[string]bool
	count  int
}

// stallNudge returns a nudge for the context the next request will see, or
// nil (see stallCheck).
func (s *Session) stallNudge(context []agent.AgentMessage) []agent.AgentMessage {
	if s.efficiency == nil || !s.efficiency.cfg.StallNudge || s.stall.count >= stallMaxNudges {
		return nil
	}
	key, text := stallCheck(context, s.fileWatch.changedFile, time.Now())
	if key == "" {
		return nil
	}
	if s.stall.nudged == nil {
		s.stall.nudged = map[string]bool{}
	}
	if s.stall.nudged[key] {
		return nil
	}
	s.stall.nudged[key] = true
	s.stall.count++
	if model := s.activeModel(); model != nil {
		s.learner().Stalled(modelSpec(model), compaction.EstimateMessagesTokens(context), model.Capabilities.ContextWindow)
	}
	return []agent.AgentMessage{{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": stallMessageType,
		"content":    text,
		"display":    true,
		"timestamp":  time.Now().UnixMilli(),
	}}}
}

// stallCheck looks at the tool calls since the last user prompt and returns
// a nudge's key and text, or "" when there is none: the same call made
// stallRepeats times with no file change in between, or stallIdleTurns
// turns and stallIdleTime without progress. Progress is a file change (by a
// file tool, or a shell command that changed a watched file), a search,
// fetch, read, or read-only command the run hasn't made before, or a shell
// command whose output the run hasn't seen before.
func stallCheck(context []agent.AgentMessage, changedFile func(callID string) bool, now time.Time) (key, text string) {
	start := 0
	var since int64 // milliseconds of the last progress, or of the prompt
	for i, m := range context {
		if m.User != nil {
			start, since = i+1, m.User.Timestamp
		}
	}
	// The clock starts at the model's first reply to the prompt, so time
	// spent before it (a war council, a long first think) isn't idle.
	for _, m := range context[start:] {
		if m.Assistant != nil {
			since = m.Assistant.Timestamp
			break
		}
	}
	// outputs holds each successful shell call's output, by call ID: a
	// command that prints something the run hasn't seen (a new fit, a new
	// count) found new information, even if it isn't read-only.
	outputs := map[string]string{}
	for _, m := range context[start:] {
		if r := m.ToolResult; r != nil && r.ToolName == "bash" && !r.IsError {
			var text strings.Builder
			for _, block := range r.Content {
				if t, ok := block.(ai.TextContent); ok {
					text.WriteString(t.Text)
				}
			}
			if strings.TrimSpace(text.String()) != "" {
				outputs[r.ToolCallID] = text.String()
			}
		}
	}
	calls := map[string]int{}
	seen := map[string]bool{}
	var last string
	idle := 0
	// idleTime adds each idle turn's gap, capped, so one long command
	// (a five-minute probe) can't make up the idle time on its own.
	var idleTime time.Duration
	prev := since
	gap := func(to int64) time.Duration {
		if prev == 0 || to < prev {
			return 0
		}
		return min(time.Duration(to-prev)*time.Millisecond, stallGapCap)
	}
	for _, m := range context[start:] {
		if m.Assistant == nil {
			continue
		}
		turnGap := gap(m.Assistant.Timestamp)
		prev = m.Assistant.Timestamp
		changed, found := false, false
		for _, block := range m.Assistant.Content {
			call, ok := block.(ai.ToolCall)
			if !ok {
				continue
			}
			args, _ := json.Marshal(call.Arguments)
			last = call.Name + " " + string(args)
			calls[last]++
			changed = changed || fileChangingTools[call.Name] || changedFile(call.ID)
			if field, ok := researchTools[call.Name]; ok {
				if target, _ := call.Arguments[field].(string); target != "" && !seen[call.Name+" "+target] {
					seen[call.Name+" "+target] = true
					found = true
				}
			}
			// A read-only shell command not run before in this run (rg, sed
			// -n, cat, git log) is exploring, like a read.
			if command, _ := call.Arguments["command"].(string); call.Name == "bash" && readOnlyCommandRE.MatchString(command) && !seen["bash "+command] {
				seen["bash "+command] = true
				found = true
			}
			if out, ok := outputs[call.ID]; ok && call.Name == "bash" {
				key := fmt.Sprintf("output %x", sha256.Sum256([]byte(out)))
				if !seen[key] {
					seen[key] = true
					found = true
				}
			}
		}
		switch {
		case changed:
			// A repeat after a file change can see a new result.
			idle, idleTime = 0, 0
			clear(calls)
		case found:
			idle, idleTime = 0, 0
		default:
			idle++
			idleTime += turnGap
		}
	}
	elapsed := idleTime + gap(now.UnixMilli())
	switch {
	case last != "" && calls[last] >= stallRepeats:
		return "repeat " + last, fmt.Sprintf("You have made this exact tool call %d times without changing a file in between: %s. Its result will not change. Step back, say what you learned, and try a different approach, or finish if the task is done.", calls[last], truncateRunes(last, 200))
	case idle >= stallIdleTurns && (since == 0 || elapsed >= stallIdleTime):
		spent := fmt.Sprintf("%d turns", idle)
		if since != 0 {
			spent += fmt.Sprintf(" and %d minutes", int(elapsed.Minutes()))
		}
		return fmt.Sprintf("idle %d", idle/stallIdleTurns), spent + " have passed without changing a file or finding new information. If you know what to do, do it now; if the task is done, say so and stop."
	}
	return "", ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
