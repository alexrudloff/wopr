package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// shellParams is the shell tool input: a command and an optional timeout in
// seconds. Timeout is a pointer because the tool rejects an explicit zero or negative timeout while an omitted one means none.
type shellParams struct {
	Command         string   `json:"command"`
	Timeout         *float64 `json:"timeout"`
	RunInBackground bool     `json:"run_in_background"`
}

// shellToolConfig configures a shell tool definition.
type shellToolConfig struct {
	name           string
	shellName      string
	tempFilePrefix string
	// operations executes the command.
	operations BashOperations
	// commandPrefix is prepended with "\n" to every command.
	commandPrefix string
	// exposeSessionEnvironment exposes the session environment to commands.
	exposeSessionEnvironment bool
	// binDir is prepended to PATH.
	binDir string
	// compact applies CompactShellOutput to finished commands; archive,
	// when set, stores the raw output and returns an obs_recall id.
	compact bool
	archive func(key, text string) string
	// background, when set, offers run_in_background and adopts process
	// groups a command leaves running; resolveShell starts those jobs.
	background   *BackgroundShells
	resolveShell func() (ShellConfig, error)
}

// shellToolSchema builds the shell tool's name, description, parameters,
// guidelines, and constrained sampling request.
func shellToolSchema(name, shellName string, exposeSessionEnvironment, background bool) ai.ToolSchema {
	var guidelines []string
	if exposeSessionEnvironment {
		guidelines = []string{sessionGuideline}
	}
	properties := map[string]any{
		"command": map[string]any{
			"type":        "string",
			"description": "Shell command to execute",
		},
		"timeout": map[string]any{
			"type":        "number",
			"description": "Timeout in seconds (optional, no default timeout)",
		},
	}
	if background {
		properties["run_in_background"] = map[string]any{
			"type":        "boolean",
			"description": "Start the command in the background and return at once with its id, process group, and log file. Use it for servers, watchers, renders, and other jobs that run for minutes, instead of & or nohup.",
		}
		guidelines = append(guidelines, backgroundGuideline)
	}
	return ai.ToolSchema{
		Name:             name,
		PromptGuidelines: guidelines,
		Description: "Execute a " + shellName + " command in the current working directory. Returns stdout and stderr. " +
			"Output is truncated to last " + strconv.Itoa(DefaultMaxLines) + " lines or " +
			strconv.Itoa(DefaultMaxBytes/1024) + "KB (whichever is hit first). " +
			"If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   []string{"command"},
		},
		ConstrainedSampling: strictToolSampling(),
	}
}

// backgroundGuideline tells the model how to use and follow a background
// job.
const backgroundGuideline = "For a long-running job (dev server, watcher, render, training run), use bash with run_in_background: true rather than & or nohup; read its log with tail and stop it with the kill command the result gives. When a background job ends you get a message with its exit code and last output, so don't sleep-poll it (no ps/sleep loops): continue other work or end your turn and wait. The user sees background jobs below the prompt."

// strictToolSampling is the constrained sampling the read, bash, powershell,
// edit and write definitions request: { type: "json_schema", strict: "prefer" }.
func strictToolSampling() *ai.ConstrainedSamplingConfig {
	return &ai.ConstrainedSamplingConfig{Type: "json_schema", Strict: "prefer"}
}

// jsNumber mirrors JavaScript String(number) for the finite values a JSON
// timeout carries.
func jsNumber(v float64) string {
	if abs := math.Abs(v); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		mantissa, exp, _ := strings.Cut(strconv.FormatFloat(v, 'e', -1, 64), "e")
		return mantissa + "e" + exp[:1] + strings.TrimLeft(exp[1:], "0")
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// appendShellStatus appends status to text, separated by a blank line.
func appendShellStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}

// executeShellTool runs a shell tool call: an initial empty update, then one
// operations.exec call whose raw output feeds an OutputAccumulator, with
// throttled streaming updates and abort, timeout, and exit-code statuses.
func executeShellTool(ctx context.Context, cwd string, cfg shellToolConfig, rawParams json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var p shellParams
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("%s: invalid params: %w", cfg.name, err)
	}
	command := p.Command
	if cfg.commandPrefix != "" {
		command = cfg.commandPrefix + "\n" + command
	}
	env := sessionEnvironment(ctx, cfg.exposeSessionEnvironment, cfg.binDir)
	if p.RunInBackground && cfg.background != nil && cfg.resolveShell != nil {
		return startBackground(cfg, cwd, p.Command, command, env)
	}
	output := NewOutputAccumulator(cfg.tempFilePrefix)
	var updates *shellUpdateScheduler
	if onUpdate != nil {
		updates = newShellUpdateScheduler(output, onUpdate)
		// The initial empty update creates the streaming card before any
		// output arrives.
		onUpdate("", nil)
	}
	finishOutput := func() OutputSnapshot {
		output.Finish()
		if updates != nil {
			updates.finish()
		}
		snapshot := output.Snapshot(true)
		_ = output.CloseTempFile()
		return snapshot
	}

	result, err := cfg.operations.Exec(ctx, command, cwd, BashOperationsExecOptions{
		OnData: func(data []byte) {
			output.Append(data)
			if updates != nil {
				updates.schedule()
			}
		},
		Timeout: p.Timeout,
		Env:     env,
	})
	if err != nil {
		snapshot := finishOutput()
		text, _ := formatShellOutput(snapshot, output.LastLineBytes(), "")
		switch msg := err.Error(); {
		case msg == "aborted":
			return agent.ErrorResult(appendShellStatus(text, "Command aborted")), nil
		case strings.HasPrefix(msg, "timeout:"):
			return agent.ErrorResult(appendShellStatus(text, "Command timed out after "+strings.TrimPrefix(msg, "timeout:")+" seconds")), nil
		default:
			return agent.ErrorResult(msg), nil
		}
	}
	snapshot := finishOutput()
	lastLineBytes := output.LastLineBytes()
	if cfg.compact {
		snapshot, lastLineBytes = compactSnapshot(snapshot, lastLineBytes, p.Command, cfg.archive)
	}
	text, details := formatShellOutput(snapshot, lastLineBytes, "(no output)")
	if cfg.background != nil && result.ProcessGroup > 0 {
		if job, ok := cfg.background.Adopt(p.Command, result.ProcessGroup); ok {
			text = appendShellStatus(text, fmt.Sprintf("[Still running in the background as %s (process group %d); stop it with: kill -- -%d. You'll be told when it ends; don't poll it. Next time use run_in_background: true to capture its output.]", job.ID, job.PGID, job.PGID))
		}
	}
	if result.ExitCode == nil {
		return agent.ErrorResult(appendShellStatus(text, "Command terminated without an exit code")), nil
	}
	if *result.ExitCode != 0 {
		return agent.ErrorResult(appendShellStatus(text, fmt.Sprintf("Command exited with code %d", *result.ExitCode))), nil
	}
	var resultDetails any
	if details != nil {
		resultDetails = details
	}
	return agent.AgentToolResult{Content: text, Details: resultDetails}, nil
}

// compactSnapshot compacts a finished command's output. When compaction
// removed enough to matter, one line says where the raw output is: an
// obs_recall id, the full-output file of a truncated run, or `| cat`.
func compactSnapshot(snapshot OutputSnapshot, lastLineBytes int, command string, archive func(key, text string) string) (OutputSnapshot, int) {
	raw := snapshot.Content
	compacted := CompactShellOutput(command, raw)
	if compacted == raw {
		return snapshot, lastLineBytes
	}
	if removed := len(raw) - len(compacted); removed >= compactNoteMinBytes {
		where := "rerun with | cat for raw output"
		if id := callArchive(archive, command, raw); id != "" {
			where = fmt.Sprintf(`raw: obs_recall {"id":"%s"}`, id)
		} else if snapshot.FullOutputPath != "" {
			where = "raw: " + snapshot.FullOutputPath
		}
		compacted += fmt.Sprintf("\n[compacted %d→%d lines; %s]", strings.Count(raw, "\n")+1, strings.Count(compacted, "\n")+1, where)
	}
	snapshot.Content = compacted
	if i := strings.LastIndexByte(compacted, '\n'); i >= 0 {
		lastLineBytes = len(compacted) - i - 1
	} else {
		lastLineBytes = len(compacted)
	}
	return snapshot, lastLineBytes
}

func callArchive(archive func(key, text string) string, key, text string) string {
	if archive == nil {
		return ""
	}
	return archive(key, text)
}

// formatShellOutput returns the output (or emptyText),
// plus the truncation notice naming the full-output file when truncated.
func formatShellOutput(snapshot OutputSnapshot, lastLineBytes int, emptyText string) (string, *BashDetails) {
	text := cmp.Or(snapshot.Content, emptyText)
	tr := snapshot.Truncation
	if !tr.Truncated {
		return text, nil
	}
	details := &BashDetails{Truncation: &tr, FullOutputPath: snapshot.FullOutputPath}
	startLine := tr.TotalLines - tr.OutputLines + 1
	endLine := tr.TotalLines
	switch {
	case tr.LastLinePartial:
		text += fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s). Full output: %s]",
			FormatSize(tr.OutputBytes), endLine, FormatSize(lastLineBytes), snapshot.FullOutputPath)
	case tr.TruncatedBy == "lines":
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]",
			startLine, endLine, tr.TotalLines, snapshot.FullOutputPath)
	default:
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]",
			startLine, endLine, tr.TotalLines, FormatSize(DefaultMaxBytes), snapshot.FullOutputPath)
	}
	return text, details
}

// startBackground starts a run_in_background command and reports where its
// output goes and how to stop it.
func startBackground(cfg shellToolConfig, cwd, command, resolved string, env []string) (agent.AgentToolResult, error) {
	if _, err := os.Stat(cwd); err != nil {
		return agent.ErrorResult("Working directory does not exist: " + cwd), nil
	}
	shell, err := cfg.resolveShell()
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	if env == nil {
		env = GetShellEnv(cfg.binDir)
	}
	job, err := cfg.background.Start(resolved, cwd, env, shell)
	if err != nil {
		return agent.ErrorResult("Could not start the background job: " + err.Error()), nil
	}
	job.Command = command
	cfg.background.setCommand(job.ID, command)
	return agent.AgentToolResult{
		Content: fmt.Sprintf("Started in the background as %s (process group %d). You'll get a message with its exit code and last output when it ends; don't poll it.\nOutput: %s\nRead it with: tail -n 50 %s\nStop it with: kill -- -%d",
			job.ID, job.PGID, job.LogPath, job.LogPath, job.PGID),
		Details: map[string]any{"background": job.ID, "pgid": job.PGID, "log": job.LogPath},
	}, nil
}
