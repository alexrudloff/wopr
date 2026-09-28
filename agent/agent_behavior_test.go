package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// Agent behavior tests. Event listeners are an EventCh consumer
// (eventRecorder), and cancellation is the run's context.

// busyAgent starts a prompt whose response ends only when its context is
// cancelled, and returns the cancel and wait functions.
func busyAgent(t *testing.T) (*Agent, func()) {
	t.Helper()
	started := make(chan struct{}, 1)
	a := NewAgent(AgentOptions{Model: scriptedModel(&scriptedProvider{respond: func(_ int, req scriptedRequest) *ai.AssistantMessageEventStream {
		return abortableStream(req.ctx, started)
	}})})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Send(ctx, "First message")
	}()
	waitSignal(t, started, "stream start")
	return a, func() {
		cancel()
		waitSignal(t, done, "first prompt")
	}
}

func TestAgent_ThrowsWhenPromptCalledWhileStreaming(t *testing.T) {
	a, stop := busyAgent(t)
	defer stop()

	if !a.IsStreaming() {
		t.Fatal("agent is not streaming")
	}
	if _, err := a.Send(context.Background(), "Second message"); !errors.Is(err, ErrAlreadyProcessingPrompt) {
		t.Fatalf("second Send error = %v, want ErrAlreadyProcessingPrompt", err)
	}
}

// steerOnAssistantEnd queues steering when the assistant message ends.
func steerOnAssistantEnd(a **Agent, steering AgentMessage) *eventRecorder {
	return newEventRecorder(func(ev AgentEvent) {
		if end, ok := ev.(MessageEndEvent); ok && end.Message.Assistant != nil {
			(*a).Steer(steering)
		}
	})
}

func checkQueuesKept(t *testing.T, a *Agent, steering, followUp AgentMessage) {
	t.Helper()
	if got := a.PeekQueuedMessages(); len(got) != 1 || got[0].User != steering.User {
		t.Fatalf("queued = %v, want the steering message", roles(got))
	}
	a.ClearSteeringQueue()
	if got := a.PeekQueuedMessages(); len(got) != 1 || got[0].User != followUp.User {
		t.Fatalf("queued = %v, want the follow-up message", roles(got))
	}
}

func TestAgent_KeepsQueuesOnFailedResponseDespiteContinuation(t *testing.T) {
	for _, reason := range []ai.StopReason{ai.StopReasonError, ai.StopReasonAborted} {
		t.Run(string(reason), func(t *testing.T) {
			steering, followUp := userMessage("steering"), userMessage("follow-up")
			var a *Agent
			rec := steerOnAssistantEnd(&a, steering)
			a = NewAgent(AgentOptions{
				Model:   scriptedModel(&scriptedProvider{respond: func(int, scriptedRequest) *ai.AssistantMessageEventStream { return errorStream(reason) }}),
				EventCh: rec.ch,
				FinishTurn: func(context.Context, AgentTurnContext) *AgentTurnDecision {
					return &AgentTurnDecision{Action: AgentTurnContinue}
				},
			})
			a.FollowUp(followUp)

			mustSend(t, a, "start")
			rec.stop()
			checkQueuesKept(t, a, steering, followUp)
		})
	}
}
