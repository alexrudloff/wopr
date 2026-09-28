package coding

import (
	"context"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// A bodyless 400 is an overflow only for Cerebras, so an
// OpenRouter "400 Provider returned error" is a transient provider error that
// the Session retries.
func TestSessionRetriesProviderReturnedError400(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":true,"maxRetries":2,"baseDelayMs":1}}`},
		fauxError("400 Provider returned error"),
		fauxReply("recovered", ai.StopReasonStop, 0),
	)
	if _, err := h.session.Send(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	h.settle(t)
	if got := h.provider.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (one retry)", got)
	}
	if text := h.session.LastAssistantText(); text == nil || *text != "recovered" {
		t.Fatalf("last assistant text = %v, want the retried response", text)
	}
}
