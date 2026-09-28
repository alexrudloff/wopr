package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// findToolResult returns the single tool result for toolCallID from a finished
// Send message history, or fails if it is missing. The tool_use/tool_result
// linkage this asserts is the invariant that keeps the next turn's history
// un-poisoned (see D48 / NormalizeMessages): a result that loses its
// ToolCallID orphans on the next turn and the provider rejects the request.
func findToolResult(t *testing.T, msgs []AgentMessage, toolCallID string) ToolResultMessage {
	t.Helper()
	var found []ToolResultMessage
	for _, m := range msgs {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == toolCallID {
			found = append(found, *m.ToolResult)
		}
	}
	if len(found) != 1 {
		t.Fatalf("tool result for %q appeared %d times, want exactly 1: %+v", toolCallID, len(found), msgs)
	}
	return found[0]
}

// TestSend_ToolExecuteError_BecomesLinkedErrorResult drives the production agent
// loop end to end: the model requests a tool whose Execute returns a Go error.
// A failing tool must become an error tool-result the model can recover
// from; it must not crash the run or drop the turn. The loop must produce an IsError result whose ToolCallID still matches the
// call, so the tool_use/tool_result pair stays intact in history.
func TestSend_ToolExecuteError_BecomesLinkedErrorResult(t *testing.T) {
	tool := &fakeTool{name: "boom", mode: ToolModeSequential, execErr: errors.New("disk on fire")}
	prov := providerFromSeqs(
		toolCallSeq(struct{ id, name string }{"tc-err", "boom"}),
		textSeq("recovered"),
	)
	a := NewAgent(AgentOptions{Model: fakeTestModel(prov), Tools: []AgentTool{tool}, MaxTurns: 5})

	msgs, err := a.Send(context.Background(), "run boom")
	if err != nil {
		t.Fatalf("Send returned an error instead of an error tool-result: %v", err)
	}
	res := findToolResult(t, msgs, "tc-err")
	if !res.IsError {
		t.Fatalf("tool result IsError = false, want true for a thrown tool: %+v", res)
	}
	if !strings.Contains(res.Text(), "disk on fire") {
		t.Fatalf("tool result content = %q, want the tool's error message", res.Text())
	}
	if res.ToolName != "boom" {
		t.Fatalf("tool result ToolName = %q, want boom", res.ToolName)
	}
}

// TestSend_UnknownTool_BecomesLinkedErrorResult drives the loop when the model
// calls a tool that is not registered. wopr must synthesize an IsError result
// ("not found") with the calling ToolCallID rather than crash or silently drop
// the call, so the turn's tool pairing stays valid.
func TestSend_UnknownTool_BecomesLinkedErrorResult(t *testing.T) {
	prov := providerFromSeqs(
		toolCallSeq(struct{ id, name string }{"tc-ghost", "ghost"}),
		textSeq("done"),
	)
	a := NewAgent(AgentOptions{Model: fakeTestModel(prov), Tools: nil, MaxTurns: 5})

	msgs, err := a.Send(context.Background(), "call ghost")
	if err != nil {
		t.Fatalf("Send returned an error instead of a not-found tool-result: %v", err)
	}
	res := findToolResult(t, msgs, "tc-ghost")
	if !res.IsError {
		t.Fatalf("unknown-tool result IsError = false, want true: %+v", res)
	}
	if !strings.Contains(res.Text(), "not found") {
		t.Fatalf("unknown-tool result content = %q, want a not-found message", res.Text())
	}
	if res.ToolName != "ghost" {
		t.Fatalf("unknown-tool result ToolName = %q, want ghost", res.ToolName)
	}
}
