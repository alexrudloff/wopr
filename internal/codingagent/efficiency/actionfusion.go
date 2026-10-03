package efficiency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Action Fusion: an edit or write can run its follow-up validation command
// in the same tool call. The model decision between "edit" and "now run the
// tests" disappears, which removes one full provider round trip and one
// replay of the context.

const (
	ThenRunSucceeded = "[then_run:succeeded]"
	ThenRunFailed    = "[then_run:failed]"
	ThenRunSkipped   = "[then_run:skipped]"
)

const (
	editThenRunDescription  = "Command to run next on this file after the edit succeeds — e.g. run, build, start/restart, install, or check it; optional timeout in seconds. Skipped if the edit fails; a non-zero exit is reported but keeps the edit."
	writeThenRunDescription = "Command to run next on this file after the write succeeds — e.g. run, build, start/restart, install, or check it; optional timeout in seconds. Skipped if the write fails; a non-zero exit is reported but keeps the write."
)

// ThenRun is the optional follow-up command.
type ThenRun struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"`
}

// FusedTool wraps a file-mutation tool with then_run.
type FusedTool struct {
	base agent.AgentTool
	bash agent.AgentTool
	cwd  string
	// Notify reports an avoided round trip.
	Notify func(mechanism, saving string, tokens int)
}

// Fuse wraps base (edit or write) so it accepts then_run and runs it through
// bash after a successful mutation.
func Fuse(base, bash agent.AgentTool, cwd string) *FusedTool {
	return &FusedTool{base: base, bash: bash, cwd: cwd}
}

// Base returns the wrapped tool.
func (t *FusedTool) Base() agent.AgentTool { return t.base }

func (t *FusedTool) Name() string                           { return t.base.Name() }
func (t *FusedTool) Label() string                          { return t.base.Label() }
func (t *FusedTool) ExecutionMode() agent.ToolExecutionMode { return t.base.ExecutionMode() }

// ConcurrencySafe is the base tool's answer; a base that doesn't say is a
// barrier.
func (t *FusedTool) ConcurrencySafe(args json.RawMessage) bool {
	safe, ok := t.base.(agent.ConcurrencySafeTool)
	return ok && safe.ConcurrencySafe(args)
}

// Schema adds the optional then_run object to the base schema.
func (t *FusedTool) Schema() ai.ToolSchema {
	schema := t.base.Schema()
	description := editThenRunDescription
	if t.base.Name() == "write" {
		description = writeThenRunDescription
	}
	params := make(map[string]any, len(schema.Parameters))
	maps.Copy(params, schema.Parameters)
	properties := map[string]any{}
	if existing, ok := schema.Parameters["properties"].(map[string]any); ok {
		maps.Copy(properties, existing)
	}
	properties["then_run"] = map[string]any{
		"type":        "object",
		"description": description,
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Bash command to run"},
			"timeout": map[string]any{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"},
		},
		"required": []string{"command"},
	}
	params["properties"] = properties
	schema.Parameters = params
	schema.PromptGuidelines = append(append([]string(nil), schema.PromptGuidelines...),
		fmt.Sprintf("When a %s will be followed by a build, test, or run command on that file, pass it as then_run in the same call instead of a separate bash call.", t.base.Name()))
	return schema
}

// ValidationSchema is the base tool's wider validation schema plus
// then_run, when the base tool has one.
func (t *FusedTool) ValidationSchema() map[string]any {
	params := maps.Clone(t.Schema().Parameters)
	wide, ok := t.base.(agent.ValidationSchemer)
	if !ok {
		return params
	}
	properties := maps.Clone(params["properties"].(map[string]any))
	if base, ok := wide.ValidationSchema()["properties"].(map[string]any); ok {
		maps.Copy(properties, base)
	}
	params["properties"] = properties
	return params
}

// PrepareArguments forwards the base tool's normalization, keeping then_run.
func (t *FusedTool) PrepareArguments(raw json.RawMessage) (json.RawMessage, error) {
	preparer, ok := t.base.(agent.ArgumentPreparer)
	if !ok {
		return raw, nil
	}
	thenRun, stripped, err := splitThenRun(raw)
	if err != nil {
		return raw, nil
	}
	prepared, err := preparer.PrepareArguments(stripped)
	if err != nil {
		return nil, err
	}
	if thenRun == nil {
		return prepared, nil
	}
	return withThenRun(prepared, thenRun)
}

// ReserveMutationOrder forwards the base tool's queue reservation.
func (t *FusedTool) ReserveMutationOrder(raw json.RawMessage) (*agent.MutationTicket, bool) {
	orderable, ok := t.base.(agent.QueueOrderable)
	if !ok {
		return nil, false
	}
	_, stripped, err := splitThenRun(raw)
	if err != nil {
		return nil, false
	}
	return orderable.ReserveMutationOrder(stripped)
}

func splitThenRun(raw json.RawMessage) (*ThenRun, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, raw, err
	}
	encoded, present := fields["then_run"]
	if !present || string(encoded) == "null" {
		return nil, raw, nil
	}
	var thenRun ThenRun
	if err := json.Unmarshal(encoded, &thenRun); err != nil {
		return nil, raw, err
	}
	delete(fields, "then_run")
	stripped, err := json.Marshal(fields)
	if err != nil {
		return nil, raw, err
	}
	return &thenRun, stripped, nil
}

func withThenRun(raw json.RawMessage, thenRun *ThenRun) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(thenRun)
	if err != nil {
		return nil, err
	}
	fields["then_run"] = encoded
	return json.Marshal(fields)
}

var fusedQueues sync.Map // canonical path → *sync.Mutex

func fusedLock(path string) *sync.Mutex {
	key := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		key = resolved
	}
	lock, _ := fusedQueues.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return SHA256(string(data)), nil
}

func resolveToolPath(cwd, path string) string {
	path = strings.TrimPrefix(path, "@")
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// Execute applies the mutation and, when asked, runs the follow-up command
// before returning one combined observation. Both steps run under one
// per-file lock so another fused mutation of the same file cannot interleave.
func (t *FusedTool) Execute(ctx context.Context, toolCallID string, params json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	thenRun, stripped, err := splitThenRun(params)
	if err != nil {
		return t.base.Execute(ctx, toolCallID, params, onUpdate)
	}
	if thenRun == nil {
		return t.base.Execute(ctx, toolCallID, stripped, onUpdate)
	}
	var target struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(stripped, &target)
	absolute := resolveToolPath(t.cwd, target.Path)
	lock := fusedLock(absolute)
	lock.Lock()
	defer lock.Unlock()

	mutation, err := t.base.Execute(ctx, toolCallID, stripped, onUpdate)
	if err != nil || mutation.IsError {
		message := mutation.Content
		if err != nil {
			message = err.Error()
		}
		return agent.AgentToolResult{
			Content: message + "\n\n" + ThenRunSkipped + " The file mutation did not complete successfully; the command was not run.",
			Details: mutation.Details,
			IsError: true,
		}, nil
	}
	if err := assertUnchangedBeforeCommand(absolute); err != nil {
		return agent.AgentToolResult{Content: mutation.Content + "\n\n" + err.Error(), Details: mutation.Details, IsError: true}, nil
	}
	if strings.TrimSpace(thenRun.Command) == "" {
		return agent.AgentToolResult{Content: mutation.Content + "\n\n" + ThenRunSkipped + " then_run.command is empty; the command was not run.", Details: mutation.Details, IsError: true}, nil
	}
	command, err := json.Marshal(map[string]any{"command": thenRun.Command, "timeout": thenRun.Timeout})
	if err != nil {
		return mutation, nil
	}
	bashResult, bashErr := t.bash.Execute(ctx, toolCallID+":then_run", command, nil)
	if bashErr != nil || bashResult.IsError {
		detail := bashResult.Content
		if bashErr != nil {
			detail = bashErr.Error()
		}
		parts := []string{mutation.Content, ThenRunFailed, detail}
		return agent.AgentToolResult{Content: strings.Join(nonEmpty(parts), "\n\n"), Details: mutation.Details, IsError: true}, nil
	}
	output := bashResult.Content
	combined := ThenRunSucceeded
	if strings.TrimSpace(output) != "" {
		combined += "\n" + output
	}
	if t.Notify != nil {
		t.Notify("Action Fusion", "1 model round-trip avoided", 0)
	}
	// Keep the mutation's own details (the TUI renders the edit diff from
	// them); a truncated command output names its full-output file inline.
	return agent.AgentToolResult{Content: mutation.Content + "\n" + combined, Details: mutation.Details}, nil
}

func nonEmpty(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// assertUnchangedBeforeCommand hashes the target twice with a scheduling
// point in between and refuses to run the command if the content changed.
func assertUnchangedBeforeCommand(path string) error {
	first, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("%s %w; the command was not run.", ThenRunSkipped, err)
	}
	// Give any concurrent writer a chance to land before the check.
	ch := make(chan struct{})
	go func() { close(ch) }()
	<-ch
	second, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("%s %w; the command was not run.", ThenRunSkipped, err)
	}
	if first != second {
		return errors.New(ThenRunSkipped + " target content changed after the fused mutation; the command was not run.")
	}
	return nil
}

// ThenRunCommand extracts then_run.command from raw tool arguments.
func ThenRunCommand(raw json.RawMessage) string {
	thenRun, _, err := splitThenRun(raw)
	if err != nil || thenRun == nil {
		return ""
	}
	return thenRun.Command
}
