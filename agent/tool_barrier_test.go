package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// fileTool stands in for a command that creates a file (writer) or a read of
// it (reader). Only the reader says it is concurrency-safe.
type fileTool struct {
	fakeTool
	exists *atomic.Bool
	safe   bool
}

func (t *fileTool) ConcurrencySafe(json.RawMessage) bool { return t.safe }

func (t *fileTool) Execute(context.Context, string, json.RawMessage, ToolUpdateCallback) (AgentToolResult, error) {
	if !t.safe {
		time.Sleep(50 * time.Millisecond)
		t.exists.Store(true)
		return AgentToolResult{Content: "wrote"}, nil
	}
	if !t.exists.Load() {
		return AgentToolResult{Content: "no such file", IsError: true}, nil
	}
	return AgentToolResult{Content: "read"}, nil
}

// A command that creates a file and a read of it in one message: the read
// must wait for the command (it ran first and failed with "no such file",
// 32 times in one session), and results stay in call order.
func TestBarrierCallRunsBeforeLaterReads(t *testing.T) {
	var exists atomic.Bool
	writer := &fileTool{name: "make", mode: ToolModeParallel, exists: &exists}
	reader := &fileTool{name: "look", mode: ToolModeParallel, exists: &exists, safe: true}
	calls := toolCallSeq(struct{ id, name string }{"tc-m", "make"}, struct{ id, name string }{"tc-l", "look"})
	a := NewAgent(AgentOptions{Model: fakeTestModel(providerFromSeqs(calls, textSeq("done"))), Tools: []AgentTool{writer, reader}, MaxTurns: 5})

	msgs, err := a.Send(context.Background(), "go")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var results []string
	for _, m := range msgs {
		if m.ToolResult != nil {
			results = append(results, m.ToolResult.Text())
		}
	}
	if len(results) != 2 || results[0] != "wrote" || results[1] != "read" {
		t.Fatalf("results = %v, want [wrote read]", results)
	}
}
