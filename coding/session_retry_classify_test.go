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

// providerFailure is the classification choke point both failover call sites
// use. It pins RateLimited to the shared classifier and passes the rest of the
// router failure through unchanged.
func TestProviderFailureClassifiesRateLimits(t *testing.T) {
	limited := providerFailure("free_tier_limit", 100, false)
	if !limited.RateLimited {
		t.Errorf("free_tier_limit not classified rate-limited: %+v", limited)
	}
	if limited.Message != "free_tier_limit" || limited.ContextTokens != 100 || limited.Overflow {
		t.Errorf("failure fields not passed through: %+v", limited)
	}
	if providerFailure("connection refused", 100, false).RateLimited {
		t.Error("connection refused classified rate-limited")
	}
	if overflow := providerFailure("connection refused", 200, true); overflow.RateLimited || !overflow.Overflow {
		t.Errorf("overflow pass-through: %+v", overflow)
	}
}
