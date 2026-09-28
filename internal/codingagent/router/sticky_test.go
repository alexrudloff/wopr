package router

import (
	"context"
	"strings"
	"testing"
	"time"
)

const (
	chatPrompt = "thanks"
	planPrompt = "Design the architecture and plan the migration strategy"
)

func autoRouter(t *testing.T) (*Router, *time.Time) {
	t.Helper()
	r := newTestRouter(t, withFakeJev(t, testConfig(), promptJev()), testHost())
	clock := time.Unix(1_000_000, 0)
	r.now = func() time.Time { return clock }
	return r, &clock
}

func TestOrchestratorStaysOnItsModelWhileTheCacheIsWarm(t *testing.T) {
	r, _ := autoRouter(t)
	first := r.Decide(context.Background(), Input{Prompt: planPrompt, ContextTokens: 2000})
	if first == nil || first.Tier != "subscription" {
		t.Fatalf("plan should start on a frontier subscription model, got %+v", first)
	}
	// A trivial follow-up would route to a free model if decided alone; the
	// warm session keeps the frontier model instead.
	next := r.Decide(context.Background(), Input{Prompt: chatPrompt, ContextTokens: 3000})
	if next == nil || next.Spec() != first.Spec() || !strings.HasPrefix(next.Reason, "sticky") {
		t.Fatalf("warm turn should keep %s, got %+v", first.Spec(), next)
	}
	if rank(next.Thinking) < rank(first.Thinking) {
		t.Fatalf("thinking fell mid-session: %s -> %s", first.Thinking, next.Thinking)
	}
}

func TestOrchestratorRatchetsUpMidSession(t *testing.T) {
	r, _ := autoRouter(t)
	first := r.Decide(context.Background(), Input{Prompt: chatPrompt, ContextTokens: 2000})
	if first == nil || first.Tier == "subscription" {
		t.Fatalf("chat should start on a free model, got %+v", first)
	}
	up := r.Decide(context.Background(), Input{Prompt: planPrompt, ContextTokens: 3000})
	if up == nil || up.Tier != "subscription" || !strings.HasPrefix(up.Reason, "ratchet up") {
		t.Fatalf("a harder turn should move up, got %+v", up)
	}
}

func TestOrchestratorRedecidesWhenTheCacheIsCold(t *testing.T) {
	for _, tc := range []struct {
		name string
		cool func(r *Router, clock *time.Time)
	}{
		{"idle", func(_ *Router, clock *time.Time) { *clock = clock.Add(11 * time.Minute) }},
		{"compaction", func(r *Router, _ *time.Time) { r.MarkCold() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, clock := autoRouter(t)
			first := r.Decide(context.Background(), Input{Prompt: planPrompt, ContextTokens: 2000})
			tc.cool(r, clock)
			next := r.Decide(context.Background(), Input{Prompt: chatPrompt, ContextTokens: 3000})
			if next == nil || next.Tier == first.Tier {
				t.Fatalf("cold turn should re-decide down from %s, got %+v", first.Spec(), next)
			}
		})
	}
}

func TestIdleShorterThanTheCacheLifetimeStaysWarm(t *testing.T) {
	r, clock := autoRouter(t)
	first := r.Decide(context.Background(), Input{Prompt: planPrompt, ContextTokens: 2000})
	*clock = clock.Add(9 * time.Minute)
	if next := r.Decide(context.Background(), Input{Prompt: chatPrompt}); next.Spec() != first.Spec() {
		t.Fatalf("9 idle minutes should keep %s, got %s", first.Spec(), next.Spec())
	}
}

func TestModes(t *testing.T) {
	r, _ := autoRouter(t)
	if r.Mode() != ModeAuto || !r.SubagentRouting() {
		t.Fatalf("enabled config should start in auto, got %s", r.Mode())
	}
	r.PinOrchestrator()
	if r.Mode() != ModePinned || !r.SubagentRouting() || r.Auto() {
		t.Fatalf("picking a model should pin the orchestrator and keep subagent routing, got %s", r.Mode())
	}
	if d := r.Decide(context.Background(), Input{Prompt: planPrompt}); d != nil {
		t.Fatalf("pinned orchestrator must not be routed, got %+v", d)
	}
	if _, _, ok := r.SideTask(1000); !ok {
		t.Fatal("pinned mode still routes side tasks")
	}
	r.SetMode(ModeOff)
	if _, _, ok := r.SideTask(1000); ok || r.SubagentRouting() {
		t.Fatal("off runs subagents and side tasks on the orchestrator")
	}
	r.PinOrchestrator()
	if r.Mode() != ModeOff {
		t.Fatalf("picking a model while off stays off, got %s", r.Mode())
	}
	r.SetMode(ModeAuto)
	if r.Mode() != ModeAuto {
		t.Fatalf("got %s", r.Mode())
	}
}
