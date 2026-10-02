package coding

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
)

// Final check: the first time a run that changed something would end, the
// model is asked once to check its work against the request before it
// finishes. Runs that only read and answer are left alone.

const finalCheckText = `Before you finish, check your work against the request:
1. Re-read the original request and list each explicit requirement: outputs, file names, paths, formats, numeric limits.
2. Verify each one now with a command or tool, not from memory. Run the tests or the program if there are any.
3. Leave margin on numeric thresholds instead of landing on the edge, and match the exact names, paths, and output format asked for.
4. If you renamed or patched an API across the code, search every source type the build uses (e.g. .pyx, .pxd, .c, .h, .ts, not just .py) for what's left.
5. Fix anything that fails, then finish with a one-line confirmation per requirement.`

// finalCheckState is one prompt's bookkeeping: whether the run did work,
// and whether it was already checked.
type finalCheckState struct {
	worked atomic.Bool
	done   bool
}

// finalCheckAfterToolCall marks the run as having done work: a file tool
// that succeeded, or a shell command that is not read-only.
func (s *Session) finalCheckAfterToolCall(_ context.Context, _, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
	switch {
	case fileChangingTools[toolName] && !result.IsError:
		s.final.worked.Store(true)
	case toolName == "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &in) == nil && in.Command != "" && !readOnlyCommandRE.MatchString(in.Command) {
			s.final.worked.Store(true)
		}
	}
	return agent.AfterToolCallResult{}
}

// finalCheckAtTurnEnd queues the final check when the turn ends the run
// normally (no tool calls), the run did work, and it was not checked yet.
func (s *Session) finalCheckAtTurnEnd(turn agent.AgentTurnContext) {
	if s.efficiency == nil || !s.efficiency.cfg.FinalCheck || s.final.done || !s.final.worked.Load() {
		return
	}
	if turn.Message == nil || turn.Message.StopReason != ai.StopReasonStop || len(turn.ToolResults) > 0 {
		return
	}
	if _, followUps := s.agent.PendingMessages(); len(followUps) > 0 {
		return
	}
	s.final.done = true
	s.agent.FollowUp(agent.AgentMessage{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": efficiency.FinalCheckMessageType,
		"content":    finalCheckText,
		"display":    true,
		"timestamp":  time.Now().UnixMilli(),
	}})
}
