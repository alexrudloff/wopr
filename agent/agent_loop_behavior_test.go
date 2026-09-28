package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// Agent loop tests. The agent owns the loop, so cases drive it through
// Agent.Send (or runPrompt when a case replaces the loop's queue callbacks),
// with a scriptedProvider as the stream.

func TestAgentLoop_HandlesToolCallsAndResults(t *testing.T) {
	toolUsage := &ai.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, TotalTokens: 10}
	patchedUsage := &ai.Usage{Input: 5, Output: 6, CacheRead: 7, CacheWrite: 8, TotalTokens: 26}
	var executed []string
	tool := valueEchoTool(ToolModeParallel, func(value string) { executed = append(executed, value) })
	base := tool.execute
	tool.execute = func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateCallback) (AgentToolResult, error) {
		result, err := base(ctx, id, args, onUpdate)
		result.Usage = toolUsage
		return result, err
	}
	var observedUsage *ai.Usage
	rec := newEventRecorder(nil)
	a := NewAgent(AgentOptions{
		Model:   scriptedModel(&scriptedProvider{respond: toolCallsThenText(toolCall("tool-1", "echo", ai.JsonObject{"value": "hello"}))}),
		Tools:   []AgentTool{tool},
		EventCh: rec.ch,
		AfterToolCall: []AfterToolCallHook{func(_ context.Context, _, _ string, _ json.RawMessage, result AgentToolResult) AfterToolCallResult {
			observedUsage = result.Usage
			return AfterToolCallResult{Usage: patchedUsage}
		}},
	})

	msgs := mustSend(t, a, "echo something")
	events := rec.stop()

	if !reflect.DeepEqual(executed, []string{"hello"}) {
		t.Fatalf("executed = %v, want [hello]", executed)
	}
	types := eventTypes(events)
	if !slices.Contains(types, "tool_execution_start") || !slices.Contains(types, "tool_execution_end") {
		t.Fatalf("events %v lack tool execution events", types)
	}
	for _, ev := range events {
		if end, ok := ev.(ToolExecutionEndEvent); ok && end.Result.IsError {
			t.Fatalf("tool_execution_end isError = true: %+v", end)
		}
	}
	if observedUsage != toolUsage {
		t.Fatalf("afterToolCall saw usage %+v, want the tool's %+v", observedUsage, toolUsage)
	}
	if got := findToolResult(t, msgs, "tool-1").Usage; got == nil || *got != *patchedUsage {
		t.Fatalf("tool result usage = %+v, want %+v", got, patchedUsage)
	}
}

func TestAgentLoop_DoesNotExecuteToolCallsFromLengthTruncatedMessage(t *testing.T) {
	var executed []string
	truncated := toolUseMessage(toolCall("tool-1", "echo", ai.JsonObject{"value": "hel"}))
	truncated.StopReason = ai.StopReasonLength
	provider := &scriptedProvider{respond: func(call int, _ scriptedRequest) *ai.AssistantMessageEventStream {
		if call == 1 {
			return doneStream(truncated)
		}
		return doneStream(textMessage("done"))
	}}
	rec := newEventRecorder(nil)
	a := NewAgent(AgentOptions{Model: scriptedModel(provider), Tools: []AgentTool{valueEchoTool(ToolModeParallel, func(v string) { executed = append(executed, v) })}, EventCh: rec.ch})

	msgs := mustSend(t, a, "echo something")
	events := rec.stop()

	if len(executed) != 0 {
		t.Fatalf("executed = %v, want nothing", executed)
	}
	var end *ToolExecutionEndEvent
	for _, ev := range events {
		if e, ok := ev.(ToolExecutionEndEvent); ok {
			end = &e
			break
		}
	}
	if end == nil || !end.Result.IsError || !strings.Contains(end.Result.Content, "output token limit") {
		t.Fatalf("tool_execution_end = %+v, want an output-token-limit error", end)
	}
	if provider.calls() != 2 {
		t.Fatalf("provider calls = %d, want 2 (the loop continues)", provider.calls())
	}
	if msgs[len(msgs)-1].Assistant == nil {
		t.Fatalf("last message role = %s, want assistant", msgs[len(msgs)-1].Role())
	}
}

func TestAgentLoop_InjectsQueuedMessagesAfterAllToolCallsComplete(t *testing.T) {
	var a *Agent
	var executed []string
	tool := valueEchoTool(ToolModeParallel, func(value string) {
		executed = append(executed, value)
		if value == "first" {
			a.Steer(userMessage("interrupt"))
		}
	})
	provider := &scriptedProvider{respond: toolCallsThenText(
		toolCall("tool-1", "echo", ai.JsonObject{"value": "first"}),
		toolCall("tool-2", "echo", ai.JsonObject{"value": "second"}),
	)}
	rec := newEventRecorder(nil)
	a = NewAgent(AgentOptions{Model: scriptedModel(provider), Tools: []AgentTool{tool}, ToolExecution: ToolModeSequential, EventCh: rec.ch})

	mustSend(t, a, "start")
	events := rec.stop()

	if !reflect.DeepEqual(executed, []string{"first", "second"}) {
		t.Fatalf("executed = %v, want [first second]", executed)
	}
	var ends int
	var sequence []string
	for _, ev := range events {
		switch ev := ev.(type) {
		case ToolExecutionEndEvent:
			ends++
			if ev.Result.IsError {
				t.Fatalf("tool_execution_end isError: %+v", ev)
			}
		case MessageStartEvent:
			if ev.Message.ToolResult != nil {
				sequence = append(sequence, "tool:"+ev.Message.ToolResult.ToolCallID)
			} else if ev.Message.User != nil {
				sequence = append(sequence, ev.Message.User.Content[0].(ai.TextContent).Text)
			}
		}
	}
	if ends != 2 {
		t.Fatalf("tool_execution_end events = %d, want 2", ends)
	}
	interrupt := slices.Index(sequence, "interrupt")
	if interrupt < 0 || slices.Index(sequence, "tool:tool-1") > interrupt || slices.Index(sequence, "tool:tool-2") > interrupt {
		t.Fatalf("message sequence = %v, want the interrupt after both tool results", sequence)
	}
	if !slices.Contains(userTexts(provider.request(2).transcript), "interrupt") {
		t.Fatal("the second request lacks the interrupt message")
	}
}

// overlapProbe records whether a "second" call ran while the "first" call
// was still pending. The first call waits for the second call or a short
// timeout, so a sequential batch still completes.
type overlapProbe struct {
	mu              sync.Mutex
	firstResolved   bool
	overlapObserved bool
	secondRan       chan struct{}
	once            sync.Once
	order           []string
}

func newOverlapProbe() *overlapProbe { return &overlapProbe{secondRan: make(chan struct{})} }

func (p *overlapProbe) run(name, value string) {
	p.mu.Lock()
	p.order = append(p.order, name+":"+value)
	p.mu.Unlock()
	switch value {
	case "first", "a":
		select {
		case <-p.secondRan:
		case <-time.After(50 * time.Millisecond):
		}
		p.mu.Lock()
		p.firstResolved = true
		p.mu.Unlock()
	default:
		p.mu.Lock()
		p.overlapObserved = !p.firstResolved
		p.mu.Unlock()
		p.once.Do(func() { close(p.secondRan) })
	}
}

func (p *overlapProbe) tool(name string, mode ToolExecutionMode) *scriptTool {
	return &scriptTool{name: name, params: valueSchema, mode: mode, execute: func(_ context.Context, _ string, args json.RawMessage, _ ToolUpdateCallback) (AgentToolResult, error) {
		p.run(name, argValue(args))
		return AgentToolResult{Content: name + ": " + argValue(args)}, nil
	}}
}

func TestAgentLoop_ForcesSequentialWhenToolIsSequentialUnderParallelConfig(t *testing.T) {
	probe := newOverlapProbe()
	a := NewAgent(AgentOptions{
		Model: scriptedModel(&scriptedProvider{respond: toolCallsThenText(
			toolCall("tool-1", "slow", ai.JsonObject{"value": "first"}),
			toolCall("tool-2", "slow", ai.JsonObject{"value": "second"}),
		)}),
		Tools: []AgentTool{probe.tool("slow", ToolModeSequential)},
	})

	msgs := mustSend(t, a, "run both")

	if probe.overlapObserved {
		t.Fatal("the second call started before the first finished")
	}
	var ids []string
	for _, m := range msgs {
		if m.ToolResult != nil {
			ids = append(ids, m.ToolResult.ToolCallID)
		}
	}
	if !reflect.DeepEqual(ids, []string{"tool-1", "tool-2"}) {
		t.Fatalf("tool results = %v, want [tool-1 tool-2]", ids)
	}
}
