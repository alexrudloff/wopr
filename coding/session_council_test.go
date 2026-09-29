package coding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

type councilTestHost map[string]bool

func (h councilTestHost) ModelInfo(provider, model string) (router.ModelInfo, bool) {
	return router.ModelInfo{DisplayName: model, ContextWindow: 200000, MaxOutputTokens: 16000}, h[provider+"/"+model]
}

func (councilTestHost) APIKey(string) string { return "" }

// The war council asks everyone but the orchestrator, keeps to private
// connections in private mode, skips members that can't answer, and drops a
// late member without waiting for it.
func TestWarCouncilMembership(t *testing.T) {
	cfg := router.DefaultConfig()
	cfg.Tiers = []router.TierConfig{
		{Name: "subscription", Cost: router.CostSubscription, Models: []router.ModelRef{
			{Provider: "anthropic", Model: "lead"}, {Provider: "openai-codex", Model: "second"}, {Provider: "openai-codex", Model: "signed-out"},
		}},
		{Name: "box", Cost: router.CostFreeRemote, ProbeURL: "http://127.0.0.1:1/v1/models", Models: []router.ModelRef{{Provider: "box", Model: "down"}}},
		{Name: "home", Cost: router.CostFreeLocal, Models: []router.ModelRef{{Provider: "home", Model: "local"}}},
	}
	cfg.PrivateProviders = []string{"box", "home"}
	r := router.New(cfg, councilTestHost{"anthropic/lead": true, "openai-codex/second": true, "box/down": true, "home/local": true})

	summary := func(members []router.CouncilMember) string {
		var out []string
		for _, m := range members {
			out = append(out, m.Ref.Spec()+"="+m.Skip)
		}
		return strings.Join(out, " ")
	}
	if got, want := summary(r.CouncilMembers(false, "anthropic/lead")), "openai-codex/second= openai-codex/signed-out=not signed in box/down=unreachable home/local="; got != want {
		t.Fatalf("members = %q, want %q", got, want)
	}
	if got, want := summary(r.CouncilMembers(true, "anthropic/lead")), "box/down=unreachable home/local="; got != want {
		t.Fatalf("private members = %q, want %q", got, want)
	}

	start := time.Now()
	proposals, missing := gatherCouncil(context.Background(), []councilCall{
		{name: "fast", spec: "home/local", run: func(context.Context) (string, error) { return "plan A", nil }},
		// A provider that never notices cancellation.
		{name: "slow", spec: "box/down", run: func(context.Context) (string, error) { time.Sleep(10 * time.Second); return "too late", nil }},
	}, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for a late member", elapsed)
	}
	if len(proposals) != 1 || proposals[0].text != "plan A" {
		t.Fatalf("proposals = %+v, want only the fast one", proposals)
	}
	if len(missing) != 1 || !strings.Contains(missing[0], "slow (late") {
		t.Fatalf("missing = %q, want the slow member dropped as late", missing)
	}
}
