package coding

import (
	"context"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// TestSessionFailoverAdvancesToTheNextRoutedModel drives a routed run whose
// first model fails with a quota error and whose second answers: the run must
// advance to the next routed model (test-faux/faux-2, Fallback true) rather
// than retrying the failed one, and the failed attempt must be omitted from
// context.
func TestSessionFailoverAdvancesToTheNextRoutedModel(t *testing.T) {
	t.Setenv("WOPR_TEST_FAUX", "1")
	h := newRecoveryHarness(t, harnessOptions{router: `{
  "enabled": true,
  "tiers": [
    {"name": "test", "cost": "free-local", "capability": 0.5,
     "models": [
       {"provider": "test-faux", "model": "faux-1"},
       {"provider": "test-faux", "model": "faux-2"}
     ]}
  ]
}`})

	if _, err := h.session.Send(context.Background(), "Trigger: free tier limit"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := h.settle(t)

	firstIndex, fallbackIndex := -1, -1
	for i, event := range events {
		route, ok := event.(agent.RouteEvent)
		if !ok {
			continue
		}
		switch {
		case route.Model == "faux-1" && !route.Fallback && firstIndex < 0:
			firstIndex = i
		case route.Model == "faux-2" && route.Fallback && fallbackIndex < 0:
			fallbackIndex = i
		}
	}
	if firstIndex < 0 {
		t.Fatal("no RouteEvent for the first routed model test-faux/faux-1")
	}
	if fallbackIndex < 0 {
		t.Fatal("no fallback RouteEvent for test-faux/faux-2")
	}
	if fallbackIndex <= firstIndex {
		t.Fatalf("fallback route (index %d) did not follow the first route (index %d)", fallbackIndex, firstIndex)
	}
	if !projectionContains(h, "failover-ok") {
		t.Fatal("projection does not contain the second attempt's text failover-ok")
	}
	if len(h.entries("context_edit")) < 1 {
		t.Fatal("the failed attempt was not omitted from context")
	}
}

// TestSessionFailoverRestsTheFailedModelForQuotaBackoff drives a routed run
// whose first model fails with a free-tier quota error and whose second
// answers: the failed model must rest for quotaBackoffMinutes (15m here), not
// the generic 60s cooldown, so it is not re-offered once 61s have elapsed.
func TestSessionFailoverRestsTheFailedModelForQuotaBackoff(t *testing.T) {
	t.Setenv("WOPR_TEST_FAUX", "1")
	h := newRecoveryHarness(t, harnessOptions{router: `{
  "enabled": true,
  "quotaBackoffMinutes": 15,
  "tiers": [
    {"name": "test", "cost": "free-local", "capability": 0.5,
     "models": [
       {"provider": "test-faux", "model": "faux-1"},
       {"provider": "test-faux", "model": "faux-2"}
     ]}
  ]
}`})

	clock := time.Now()
	h.session.router.SetClockForTest(func() time.Time { return clock })

	if _, err := h.session.Send(context.Background(), "Trigger: free tier limit"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.settle(t)

	rs := h.session.route.Load()
	if rs == nil || rs.decision == nil || rs.decision.Model != "faux-2" {
		t.Fatalf("after failover route = %+v, want the fallback decision on faux-2", rs)
	}
	if !rs.decision.At.Equal(clock) {
		t.Fatalf("SetClockForTest not in effect: decision At=%v clock=%v", rs.decision.At, clock)
	}

	// 61s is past the generic 60s cooldown, well inside quotaBackoffMinutes (15m).
	clock = clock.Add(61 * time.Second)
	next := h.session.router.Next(rs.decision, router.Failure{Message: "cooldown probe"})
	if next != nil && next.Model == "faux-1" {
		t.Fatalf("faux-1 re-offered after 61s: rested 60s, not quotaBackoffMinutes (15m)")
	}
}
