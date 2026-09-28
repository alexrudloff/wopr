package agent

import (
	"context"
	"errors"
	"testing"
)

// The loop has no default turn cap: after tool results it always makes the
// next request until the model stops calling tools. A default MaxTurns of 100
// once made the loop break out at that count, emit agent_end and return nil
// with the last message a tool result: the run ended silently
// mid-task, with no response and no error. Worker sessions show it twice at
// exactly 100 turns (tui-md 23:57Z, next-share-gateway 01:32Z), each needing a
// manual "continue".
func TestSend_DefaultHasNoTurnCap(t *testing.T) {
	const toolTurns = 105
	prov := providerFromSeqs()
	for range toolTurns {
		prov.seqs = append(prov.seqs, toolCallSeq(struct{ id, name string }{"c", "t"}))
	}
	prov.seqs = append(prov.seqs, textSeq("done"))
	a := NewAgent(AgentOptions{Model: fakeTestModel(prov), Tools: []AgentTool{&fakeTool{name: "t", mode: ToolModeSequential, content: "ok"}}})

	msgs, err := a.Send(context.Background(), "go")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.Assistant == nil || panicTestAssistantText(last.Assistant) != "done" {
		t.Fatalf("run ended on %+v after %d messages; want the model's final response after %d tool turns", last, len(msgs), toolTurns)
	}
}

// An explicit cap still stops the loop, but visibly: the run returns
// ErrMaxTurnsReached instead of ending as if the task were complete.
func TestSend_ExplicitTurnCapIsAnError(t *testing.T) {
	prov := providerFromSeqs(
		toolCallSeq(struct{ id, name string }{"c1", "t"}),
		toolCallSeq(struct{ id, name string }{"c2", "t"}),
		textSeq("done"),
	)
	a := NewAgent(AgentOptions{Model: fakeTestModel(prov), Tools: []AgentTool{&fakeTool{name: "t", mode: ToolModeSequential, content: "ok"}}, MaxTurns: 1})
	msgs, err := a.Send(context.Background(), "go")
	if !errors.Is(err, ErrMaxTurnsReached) {
		t.Fatalf("Send err = %v, want ErrMaxTurnsReached", err)
	}
	if last := msgs[len(msgs)-1]; last.ToolResult == nil {
		t.Fatalf("last message = %+v, want the unanswered tool result", last)
	}
}
