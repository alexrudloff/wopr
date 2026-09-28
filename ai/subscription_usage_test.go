package ai

import (
	"net/http"
	"testing"
	"time"
)

func TestSubscriptionUsageHeaders(t *testing.T) {
	t.Cleanup(func() {
		subscriptionUsage.mu.Lock()
		clear(subscriptionUsage.plans)
		subscriptionUsage.mu.Unlock()
	})
	now := time.Unix(1_800_000_000, 0)

	codex := http.Header{}
	codex.Set("x-codex-primary-used-percent", "42.4")
	codex.Set("x-codex-primary-window-minutes", "300")
	codex.Set("x-codex-primary-reset-after-seconds", "7200")
	codex.Set("x-codex-secondary-used-percent", "12")
	codex.Set("x-codex-secondary-window-minutes", "10080")
	codex.Set("x-codex-secondary-reset-at", "1800086400")
	recordSubscriptionHeaders(codex, now)

	claude := http.Header{}
	claude.Set("anthropic-ratelimit-unified-5h-utilization", "0.37")
	claude.Set("anthropic-ratelimit-unified-5h-reset", "1800003600")
	claude.Set("anthropic-ratelimit-unified-7d-utilization", "1.5")
	recordSubscriptionHeaders(claude, now)

	recordSubscriptionHeaders(http.Header{"Content-Type": {"text/event-stream"}}, now)

	got := SubscriptionUsage()
	if len(got) != 2 || got[0].Plan != SubscriptionChatGPT || got[1].Plan != SubscriptionClaude {
		t.Fatalf("plans = %+v", got)
	}
	want := []UsageWindow{
		{Label: "5h", UsedPercent: 42.4, ResetsAt: now.Add(2 * time.Hour)},
		{Label: "wk", UsedPercent: 12, ResetsAt: now.Add(24 * time.Hour)},
	}
	for i, w := range want {
		if g := got[0].Windows[i]; g.Label != w.Label || g.UsedPercent != w.UsedPercent || !g.ResetsAt.Equal(w.ResetsAt) {
			t.Errorf("ChatGPT window %d = %+v, want %+v", i, g, w)
		}
	}
	if g := got[1].Windows; len(g) != 2 || g[0].Label != "5h" || g[0].UsedPercent != 37 || !g[0].ResetsAt.Equal(now.Add(time.Hour)) ||
		g[1].Label != "7d" || g[1].UsedPercent != 100 || !g[1].ResetsAt.IsZero() {
		t.Errorf("Claude windows = %+v", g)
	}
}

func TestCodexRateLimitsFrame(t *testing.T) {
	t.Cleanup(func() {
		subscriptionUsage.mu.Lock()
		clear(subscriptionUsage.plans)
		subscriptionUsage.mu.Unlock()
	})
	mapped, err := mapCodexWebSocketEventFrame([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":80,"window_minutes":300,"reset_after_seconds":60}}}`))
	if err != nil || mapped.terminal {
		t.Fatalf("mapped = %+v, %v", mapped, err)
	}
	got := SubscriptionUsage()
	if len(got) != 1 || len(got[0].Windows) != 1 || got[0].Windows[0].Label != "5h" || got[0].Windows[0].UsedPercent != 80 {
		t.Fatalf("usage = %+v", got)
	}
}
