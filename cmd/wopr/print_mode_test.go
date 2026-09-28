package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/ai/aitest"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/testbudget"
)

// printModeTestHost builds a print-mode runtime whose session talks to
// provider in fresh, isolated directories.
func printModeTestHost(t *testing.T, provider ai.Provider, tools ...agent.AgentTool) printModeRuntime {
	t.Helper()
	services, err := coding.NewServices(coding.ServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return printModeRuntime{
		Services: services,
		Session: coding.SessionStartOptions{
			Model: &ai.Model{
				ID: "faux-1", Provider: provider,
				ProviderMeta: ai.ProviderMetadata{ProviderID: provider.ID()},
				Capabilities: ai.ModelCapabilities{ContextWindow: 200000, MaxOutputTokens: 8192, SupportsToolUse: true},
			},
			SkipBuiltinTools: true,
			ExtraTools:       tools,
			SessionDir:       t.TempDir(),
		},
	}
}

// printModeTestResult is one in-process print-mode run.
type printModeTestResult struct {
	stdout, stderr string
	err            error
}

// runPrintModeForTest runs print mode in process and fails the test if it
// does not return within the test budget.
func runPrintModeForTest(t *testing.T, host printModeRuntime, opts printModeOptions) printModeTestResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts.stdout, opts.stderr = &stdout, &stderr
	done := make(chan error, 1)
	go func() { done <- runPrintMode(context.Background(), host, opts) }()
	select {
	case err := <-done:
		return printModeTestResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
	case <-time.After(testbudget.Wait(t)):
		t.Fatal("print mode did not return: the run is deadlocked")
		return printModeTestResult{}
	}
}

// A json run streams one JSON object per line and ends with the answer. The
// many one-character deltas outnumber the session's event buffers.
func TestPrintModeJSONRun(t *testing.T) {
	provider := aitest.NewFauxProvider("", "")
	provider.SetResponses(fauxTextResponse(strings.Repeat("x", 2000)))
	result := runPrintModeForTest(t, printModeTestHost(t, provider), printModeOptions{Mode: "json", InitialMessage: "stream a long answer"})
	if result.err != nil {
		t.Fatalf("err = %v, stderr %q", result.err, result.stderr)
	}
	types := map[string]bool{}
	for line := range strings.Lines(result.stdout) {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("non-JSON line %q: %v", line, err)
		}
		types[fmt.Sprint(event["type"])] = true
	}
	if !types["message_update"] || !types["agent_end"] {
		t.Fatalf("event types = %v", types)
	}
}

// printTestTool is a sequential tool that always succeeds.
type printTestTool struct{}

func (printTestTool) Name() string                           { return "print_test_tool" }
func (printTestTool) Label() string                          { return "print test tool" }
func (printTestTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }
func (printTestTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{Name: "print_test_tool", Parameters: map[string]any{"type": "object"}}
}
func (printTestTool) Execute(context.Context, string, json.RawMessage, agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	return agent.AgentToolResult{Content: "tool output"}, nil
}

func fauxTextResponse(text string) aitest.Response {
	return aitest.Response{Content: []aitest.Block{aitest.Text(text)}, StopReason: "stop"}
}

// TestPrintModePrintsOnlyTheFinalMessage: text output is only the text of the
// session's last message, so tool-use narration from earlier assistant
// messages of the run never reaches stdout ahead of the answer.
func TestPrintModePrintsOnlyTheFinalMessage(t *testing.T) {
	provider := aitest.NewFauxProvider("", "")
	provider.SetResponses(
		aitest.Response{Content: []aitest.Block{
			aitest.Text("I'll run the tool first."),
			aitest.ToolCall("print_test_tool", map[string]any{}, "print-call-1"),
		}, StopReason: "toolUse"},
		fauxTextResponse("final answer"),
	)
	result := runPrintModeForTest(t, printModeTestHost(t, provider, printTestTool{}), printModeOptions{
		Mode: "text", InitialMessage: "use the tool",
	})
	if result.err != nil {
		t.Fatalf("err = %v, stderr %q", result.err, result.stderr)
	}
	if result.stdout != "final answer\n" {
		t.Fatalf("stdout = %q, want only the final message %q", result.stdout, "final answer\n")
	}
}
