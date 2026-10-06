package coding

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
)

// Final check: the first time a run that changed something would end, the
// model is asked once to check its work against the request before it
// finishes. Runs that only read and answer, or only changed prose files
// (notes, docs), are left alone. The transcript shows it as one line, and
// the reply after it is the model's usual answer, not a list of what it
// checked.

const finalCheckText = `Before you finish, check the result against the request:
1. Re-read the request and list what it asked for: the outputs, and any names, paths, formats, or limits it gave.
2. Check each against what exists now, not from memory: open what you wrote, and run the program or its tests if the work has any.
3. Where the request gives a numeric limit, leave margin instead of landing on the edge. If you renamed or patched an API across code, search every source type the build uses (e.g. .pyx, .pxd, .c, .h, .ts, not just .py) for what's left.
4. Fix anything that falls short.
Then end with your reply to the user as you would have: what you did and what they need to know. Don't list the checks; mention one only if it found and fixed something.`

// proseExts are files whose changes need no final check: there is nothing
// to run, and the reply already says what was written.
var proseExts = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".rst": true, ".adoc": true}

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
		if slices.ContainsFunc(toolPaths("", toolName, args), func(path string) bool {
			return !proseExts[strings.ToLower(filepath.Ext(path))]
		}) {
			s.final.worked.Store(true)
		}
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
	// With a person at the frontend, background work that is still running
	// reports back and starts another run; check at the end of that one.
	if s.tasks.deliver.Load() != nil && s.backgroundWorkRunning() {
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

// backgroundWorkRunning reports a background shell job or background task
// that has not finished.
func (s *Session) backgroundWorkRunning() bool {
	for _, job := range s.bgShells.List() {
		if job.Running() {
			return true
		}
	}
	if registry := s.Agents(); registry != nil {
		return slices.ContainsFunc(registry.Running(), func(a subagent.Agent) bool { return a.Background })
	}
	return false
}
