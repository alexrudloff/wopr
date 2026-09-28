package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// panicTool panics from Execute with value.
type panicTool struct {
	name  string
	mode  ToolExecutionMode
	value any
}

func (t *panicTool) Name() string                     { return t.name }
func (t *panicTool) Label() string                    { return "" }
func (t *panicTool) Description() string              { return t.name }
func (t *panicTool) Schema() ai.ToolSchema            { return ai.ToolSchema{Name: t.name} }
func (t *panicTool) ExecutionMode() ToolExecutionMode { return t.mode }
func (t *panicTool) Execute(context.Context, string, json.RawMessage, ToolUpdateCallback) (AgentToolResult, error) {
	panic(t.value)
}

// A tool that panics must become an error result, so every tool call gets a
// result and the run continues instead of ending the process between the tool
// call and its result.
func TestSend_ToolPanic_BecomesLinkedErrorResult(t *testing.T) {
	for _, mode := range []ToolExecutionMode{ToolModeSequential, ToolModeParallel} {
		t.Run(string(mode), func(t *testing.T) {
			prov := providerFromSeqs(
				toolCallSeq(struct{ id, name string }{"tc-panic", "boom"}, struct{ id, name string }{"tc-ok", "fine"}),
				textSeq("recovered"),
			)
			a := NewAgent(AgentOptions{
				Model:    fakeTestModel(prov),
				Tools:    []AgentTool{&panicTool{name: "boom", mode: mode, value: "index out of range"}, &fakeTool{name: "fine", mode: mode, content: "ok"}},
				MaxTurns: 5,
			})

			msgs, err := a.Send(context.Background(), "run boom")
			if err != nil {
				t.Fatalf("Send returned an error instead of an error tool result: %v", err)
			}
			res := findToolResult(t, msgs, "tc-panic")
			if !res.IsError || res.Text() != "index out of range" {
				t.Fatalf("panicking tool result = %+v, want IsError with the panic message", res)
			}
			if ok := findToolResult(t, msgs, "tc-ok"); ok.IsError || ok.Text() != "ok" {
				t.Fatalf("sibling tool result = %+v, want its own success", ok)
			}
			if last := msgs[len(msgs)-1]; last.Assistant == nil || !strings.Contains(panicTestAssistantText(last.Assistant), "recovered") {
				t.Fatalf("run did not continue to the next assistant turn: %+v", last)
			}
		})
	}
}

func panicTestAssistantText(message *AssistantMessage) string {
	var text strings.Builder
	for _, block := range message.Content {
		if block, ok := block.(ai.TextContent); ok {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}
