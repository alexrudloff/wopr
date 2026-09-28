package ai

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func testAssistant(stop StopReason) *AssistantMessage {
	return &AssistantMessage{Content: []AssistantContentBlock{}, API: APIOpenAIResponses, Provider: "test", Model: "model/name", Usage: Usage{}, StopReason: stop, Timestamp: 123}
}

func pushTestStart(t *testing.T, stream *AssistantMessageEventStream) *AssistantMessage {
	t.Helper()
	partial := testAssistant(StopReasonPending)
	if err := stream.Push(StartEvent{Partial: partial}); err != nil {
		t.Fatal(err)
	}
	return partial
}

func pushTestDone(t *testing.T, stream *AssistantMessageEventStream) *AssistantMessage {
	t.Helper()
	final := testAssistant(StopReasonStop)
	if err := stream.Push(DoneEvent{Reason: StopReasonStop, Message: final}); err != nil {
		t.Fatal(err)
	}
	return final
}

func TestAssistantMessageEventStreamResultIsTerminalMessagePointerWithoutIteration(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	partial := pushTestStart(t, stream)
	final := testAssistant(StopReasonStop)
	for i := range 1000 {
		partial.Content = []AssistantContentBlock{TextContent{Text: string(rune('a' + i%26))}}
		if err := stream.Push(TextDeltaEvent{ContentIndex: 0, Delta: "x", Partial: partial}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Push(DoneEvent{Reason: StopReasonStop, Message: final}); err != nil {
		t.Fatal(err)
	}
	if result := stream.Result(); result != final {
		t.Fatalf("Result() = %p, want %p", result, final)
	}

	events := []AssistantMessageEvent{}
	for event := range stream.Events(context.Background()) {
		events = append(events, event)
	}
	done, ok := events[len(events)-1].(DoneEvent)
	if !ok || done.Message != final || len(events) != 1002 {
		t.Fatalf("terminal events = %d %#v", len(events), events[len(events)-1])
	}
}

func TestAssistantMessageEventStreamRejectsInvalidOrdering(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	partial := testAssistant(StopReasonPending)
	if err := stream.Push(TextDeltaEvent{Delta: "early", Partial: partial}); err == nil {
		t.Fatal("delta before start succeeded")
	}
	if err := stream.Push(StartEvent{Partial: partial}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Push(StartEvent{Partial: partial}); err == nil {
		t.Fatal("second start succeeded")
	}
	if err := stream.Push(DoneEvent{Reason: StopReasonError, Message: testAssistant(StopReasonError)}); err == nil {
		t.Fatal("invalid done reason succeeded")
	}
}

func TestAssistantMessageEventStreamAbandonedIterationLeavesNoWaiterOrDeliveryGoroutine(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	pushTestStart(t, stream)
	for range stream.Events(context.Background()) {
		break
	}
	stream.mu.Lock()
	waiters := len(stream.waiters)
	queued := len(stream.queue)
	stream.mu.Unlock()
	if waiters != 0 || queued != 0 {
		t.Fatalf("after abandoned iteration: waiters=%d queue=%d, want zero", waiters, queued)
	}
	pushTestDone(t, stream)
	if result := stream.Result(); result == nil {
		t.Fatal("Result() is nil after abandoned iteration")
	}
}

func TestAssistantMessageEventStreamConcurrentPushResultAndIterators(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	partial := pushTestStart(t, stream)
	var consumed atomic.Int64
	var consumers sync.WaitGroup
	consumers.Add(2)
	for range 2 {
		go func() {
			defer consumers.Done()
			for range stream.Events(context.Background()) {
				consumed.Add(1)
			}
		}()
	}

	var producers sync.WaitGroup
	for range 100 {
		producers.Go(func() {
			if err := stream.Push(TextDeltaEvent{ContentIndex: 0, Delta: "x", Partial: partial}); err != nil {
				t.Errorf("Push() = %v", err)
			}
		})
	}
	resultReady := make(chan *AssistantMessage, 1)
	go func() { resultReady <- stream.Result() }()
	producers.Wait()
	final := pushTestDone(t, stream)
	if result := <-resultReady; result != final {
		t.Fatalf("Result() = %p, want %p", result, final)
	}
	consumers.Wait()
	if got := consumed.Load(); got != 102 {
		t.Fatalf("consumed events = %d, want 102", got)
	}
}
