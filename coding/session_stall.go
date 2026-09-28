package coding

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
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
			changed = changed || fileChangingTools[call.Name]
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
