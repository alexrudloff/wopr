package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	// stallIdleTurns is how many turns without a file change count as
	// no progress.
	stallIdleTurns = 10
	// stallMaxNudges bounds the nudges in one run.
	stallMaxNudges = 3
	// stallMessageType is the custom message type of a nudge.
	stallMessageType = "stall_nudge"
)

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
// nil. It looks at the tool calls since the last user prompt: the same
// call made stallRepeats times with no file change in between, or
// stallIdleTurns turns without a file change.
func (s *Session) stallNudge(context []agent.AgentMessage) []agent.AgentMessage {
	if s.efficiency == nil || !s.efficiency.cfg.StallNudge || s.stall.count >= stallMaxNudges {
		return nil
	}
	start := 0
	for i, m := range context {
		if m.User != nil {
			start = i + 1
		}
	}
	calls := map[string]int{}
	var last string
	idle := 0
	for _, m := range context[start:] {
		if m.Assistant == nil {
			continue
		}
		changed := false
		for _, block := range m.Assistant.Content {
			call, ok := block.(ai.ToolCall)
			if !ok {
				continue
			}
			args, _ := json.Marshal(call.Arguments)
			last = call.Name + " " + string(args)
			calls[last]++
			changed = changed || fileChangingTools[call.Name] || s.fileWatch.changedFile(call.ID)
		}
		if changed {
			// A repeat after a file change can see a new result.
			idle = 0
			clear(calls)
		} else {
			idle++
		}
	}
	var key, text string
	switch {
	case last != "" && calls[last] >= stallRepeats:
		key = "repeat " + last
		text = fmt.Sprintf("You have made this exact tool call %d times without changing a file in between: %s. Its result will not change. Step back, say what you learned, and try a different approach, or finish if the task is done.", calls[last], truncateRunes(last, 200))
	case idle >= stallIdleTurns:
		key = fmt.Sprintf("idle %d", idle/stallIdleTurns)
		text = fmt.Sprintf("%d turns have passed without changing a file. If you know the fix, make it now; if the task needs no change or is done, say so and stop.", idle)
	default:
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

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
