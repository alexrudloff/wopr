package ai

import (
	"context"
	"reflect"
	"testing"
)

func collectBuilderEvents(stream *AssistantMessageEventStream) []AssistantMessageEvent {
	var events []AssistantMessageEvent
	for event := range stream.Events(context.Background()) {
		events = append(events, event)
	}
	return events
}

func TestAssistantStreamBuilderRepairsPartialToolArgumentsAndKeepsTerminalConsistent(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want JsonObject
	}{
		{name: "repairable", raw: `{"path":"partial`, want: JsonObject{"path": "partial"}},
		{name: "irreparable", raw: `{nonsense`, want: JsonObject{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := newAssistantStreamBuilder(context.Background(), APIOpenAICompletions, "test", "model", ModelCost{})
			builder.toolCallDelta(streamToolCallDelta{index: 0, id: "call", name: "read", argumentsDelta: test.raw})
			builder.done(StopReasonToolUse, nil, "")

			events := collectBuilderEvents(builder.stream)
			if len(events) != 5 {
				t.Fatalf("events = %#v, want start/tool-start/tool-delta/tool-end/done", events)
			}
			end, ok := events[3].(ToolCallEndEvent)
			if !ok || !reflect.DeepEqual(end.ToolCall.Arguments, test.want) {
				t.Fatalf("tool end = %#v, want arguments %#v", events[3], test.want)
			}
			done, ok := events[4].(DoneEvent)
			if !ok || done.Reason != StopReasonToolUse || done.Message.StopReason != done.Reason {
				t.Fatalf("terminal event = %#v", events[4])
			}
			if builder.stream.Result() != done.Message {
				t.Fatalf("result=%p done=%p", builder.stream.Result(), done.Message)
			}
		})
	}
}
