package tools

import (
	"context"
	"encoding/json"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// ─── Bash Tool ────────────────────────────────────────────────────────────────

// BashTool executes shell commands.
//
// Contract:
//   - One-shot spawn per call. cwd does NOT persist between calls.
//   - Resolved command = CommandPrefix + "\n" + command (when prefix set).
//   - Spawn via getShellConfig: settings shellPath, else /bin/bash, bash on
//     PATH, then sh (always with -c).
//   - Stream chunks: ANSI strip, binary sanitize, drop \r.
//   - Tempfile overflow at DEFAULT_MAX_BYTES (50 KB); rolling buffer 2x.
//   - On exit ≠ 0: append "Command exited with code N", return as IsError.
//     A signal-killed shell reports 128 + the signal number.
//   - On context cancel: append "Command aborted", return as IsError.
//   - Details: BashDetails{Truncation, FullOutputPath} on a truncated
//     success; error results carry none.
type BashTool struct {
	CWD string
	// Settings is consulted via GetShellConfig to resolve shellPath. nil
	// falls through to the platform default.
	Settings SettingsView
	// CommandPrefix prepended (with \n) to every command. Empty = no prefix.
	CommandPrefix string
	// BinDir (<agentDir>/bin) is prepended to the command's PATH. Empty
	// leaves PATH unchanged.
	BinDir string
	// HideSessionEnvironment hides the session environment: commands then see no WOPR_* session variables, and the prompt omits the
	// guideline that mentions them.
	HideSessionEnvironment bool
	// Compact drops known noise from the output of common commands (see
	// CompactShellOutput). Archive, when set, stores the raw output of a
	// compacted result and returns its obs_recall id.
	Compact bool
	Archive func(key, text string) string
	// Background, when set, offers run_in_background and tracks the jobs
	// a command leaves running.
	Background *BackgroundShells
}

func (t *BashTool) Name() string  { return "bash" }
func (t *BashTool) Label() string { return "" }

func (t *BashTool) Schema() ai.ToolSchema {
	return shellToolSchema("bash", "bash", !t.HideSessionEnvironment, t.Background != nil)
}

// ExecutionMode is parallel.
func (t *BashTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

// Execute runs the command through the shared shell tool execution with the
// bash config: the resolved bash, the settings command prefix, and bash temp
// files.
func (t *BashTool) Execute(ctx context.Context, _ string, rawParams json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	return executeShellTool(ctx, t.CWD, shellToolConfig{
		name:                     "bash",
		shellName:                "bash",
		tempFilePrefix:           "wopr-bash",
		operations:               &LocalShellOperations{ShellName: "bash", ResolveShell: func() (ShellConfig, error) { return GetShellConfig(t.Settings) }},
		commandPrefix:            t.CommandPrefix,
		exposeSessionEnvironment: !t.HideSessionEnvironment,
		binDir:                   t.BinDir,
		compact:                  t.Compact,
		archive:                  t.Archive,
		background:               t.Background,
		resolveShell:             func() (ShellConfig, error) { return GetShellConfig(t.Settings) },
	}, rawParams, onUpdate)
}
