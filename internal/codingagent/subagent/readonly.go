package subagent

import (
	"context"
	"encoding/json"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/shellread"
)

// Read-only shell: explore subagents get bash restricted to commands that
// only read. A command is a pipeline of allowlisted programs; chaining,
// redirection, substitution, and flags that write or run other programs are
// rejected before anything executes.

// CheckReadOnly reports why command is not a read-only pipeline, or nil.
func CheckReadOnly(command string) error { return shellread.CheckReadOnly(command) }

// readOnlyShell wraps a bash tool so only read-only pipelines run.
type readOnlyShell struct{ bash agent.AgentTool }

// ReadOnlyShell restricts bash to CheckReadOnly commands.
func ReadOnlyShell(bash agent.AgentTool) agent.AgentTool { return readOnlyShell{bash: bash} }

func (t readOnlyShell) Name() string  { return t.bash.Name() }
func (t readOnlyShell) Label() string { return t.bash.Label() }

func (t readOnlyShell) Schema() ai.ToolSchema {
	schema := t.bash.Schema()
	schema.Description = "Run one read-only command or pipeline in the working directory: rg, grep, git log/show/diff/blame/status/ls-files/grep, go doc/list, cat, head, tail, wc, ls, find, sed -n 'N,Mp'. Chaining, redirection, substitution, and anything that writes are rejected."
	schema.PromptGuidelines = nil
	return schema
}

func (t readOnlyShell) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

// ConcurrencySafe: every command it runs only reads.
func (t readOnlyShell) ConcurrencySafe(json.RawMessage) bool { return true }

func (t readOnlyShell) Execute(ctx context.Context, id string, params json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := CheckReadOnly(in.Command); err != nil {
		return agent.AgentToolResult{Content: err.Error(), IsError: true}, nil
	}
	return t.bash.Execute(ctx, id, params, onUpdate)
}
