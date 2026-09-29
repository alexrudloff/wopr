package router

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// costRouter is a router in cost mode with no subscription usage reported.
func costRouter(t *testing.T) *Router {
	t.Helper()
	r := objectiveRouter(t, ObjectiveCost)
	r.usage = func() []ai.PlanUsage { return nil }
	return r
}

// demanding is an implement prompt with demand d (no kind floor).
func demanding(complexity, capability, deep float64) Classification {
	return Classification{Kind: KindImplement, Complexity: complexity, Capability: capability, DeepReasoning: deep, Source: "test"}
}

func TestCostKeepsFreeModelsUnlessClearlyShort(t *testing.T) {
	// Demand 0.70: auto steps up to the subscription, cost stays on the local model.
	moderate := demanding(0.8, 0.75, 0)
	if d := objectiveRouter(t, ObjectiveAuto).decideWith(moderate, 4000); d == nil || d.Tier != "subscription" {
		t.Fatalf("auto should step up for demand 0.70, got %+v", d)
	}
	r := costRouter(t)
	if d := r.decideWith(moderate, 4000); d == nil || d.Spec() != "local/deepseek-v4-flash" || d.Objective != ObjectiveCost {
		t.Fatalf("cost should keep demand 0.70 on the local model, got %+v", d)
	}
	// Kind floors and the capable fallback floor are auto's preferences.
	plan := Classification{Kind: KindPlan, Complexity: 0.5, Capability: 0.5, Source: "test"}
	if d := r.decideWith(plan, 4000); d == nil || costRank(r.cfg.Tiers[d.chosen.tier].Cost) != 0 {
		t.Fatalf("cost should not apply the plan floor, got %+v", d)
	}
	// A clear shortfall steps up, to the least capable model that suffices.
	if d := r.decideWith(demanding(1, 1, 1), 4000); d == nil || d.Spec() != "anthropic/claude-sonnet-5" {
		t.Fatalf("cost should step up for demand 1.0 to sonnet, got %+v", d)
	}
}

func TestCostUsesTheSubscriptionWhenFreeModelsCannotServe(t *testing.T) {
	r := costRouter(t)
	// Too big for either free model's window.
	if d := r.decideWith(chat(), 120_000); d == nil || d.Tier != "subscription" {
		t.Fatalf("a context no free model fits should use the subscription, got %+v", d)
	}
	r.markDown("local/deepseek-v4-flash", time.Hour)
	r.markDown("lan/open-model", time.Hour)
	if d := r.decideWith(chat(), 4000); d == nil || d.Spec() != "openai-codex/gpt-6-luna" {
		t.Fatalf("with the free models down, chat should take the weakest subscription model, got %+v", d)
	}
	// A brief with no free slot moves to the subscription.
	r = costRouter(t)
	busy := func(p string) bool { return p == "local" || p == "lan" }
	chain := r.rankBrief(BriefInput{ContextTokens: 4000, Saturated: busy}, BriefClass{}, 0.4, "")
	if len(chain) == 0 || r.cfg.Tiers[chain[0].tier].Cost != CostSubscription {
		t.Fatalf("saturated free providers should yield to the subscription, got %v", specs(chain))
	}
}

func TestCostNeverPaysPerToken(t *testing.T) {
	r := costRouter(t)
	r.cfg.PaidKinds = []string{string(KindImplement)}
	// A chain from before the switch to cost still lists paid models.
	r.objective = ObjectiveAuto
	stale := &Decision{Provider: "local", Model: "deepseek-v4-flash", Tier: "local", Objective: ObjectiveAuto, chain: r.allCandidates(4000, r.Objective())}
	r.objective = ObjectiveCost
	for _, tier := range r.cfg.Tiers {
		if tier.Cost == CostPaid {
			continue
		}
		for _, ref := range tier.Models {
			r.markDown(ref.Spec(), time.Hour)
		}
	}
	if d := r.decideWith(demanding(1, 1, 1), 4000); d != nil {
		t.Fatalf("only paid models remain: want no route, got %s", d.Spec())
	}
	if d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "medium", Brief: "x", ContextTokens: 4000}); d != nil {
		t.Fatalf("only paid models remain: want no brief route, got %s", d.Spec())
	}
	if next := r.Next(stale, Failure{Message: "boom"}); next != nil {
		t.Fatalf("failover reached a paid model: %s", next.Spec())
	}
	if ref, _, ok := r.SideTask(4000); ok {
		t.Fatalf("side task reached a paid model: %s", ref.Spec())
	}
	if r.Admits(ObjectiveCost, "openrouter/anthropic/claude-opus-5.5") || r.Admits(ObjectiveCost, "someone/unlisted") {
		t.Fatal("cost must not admit a paid or unlisted model")
	}
	if !r.Admits(ObjectiveCost, "anthropic/claude-opus-5-5") || !r.Admits(ObjectiveAuto, "openrouter/anthropic/claude-opus-5.5") {
		t.Fatal("cost admits subscription models; auto admits everything")
	}
}

func TestCostEscalationStopsAtTheSubscription(t *testing.T) {
	for _, o := range []Objective{ObjectiveAuto, ObjectiveCost} {
		r := objectiveRouter(t, o)
		r.usage = func() []ai.PlanUsage { return nil }
		r.markDown("anthropic/claude-opus-5-5", time.Hour)
		r.markDown("openai-codex/gpt-6-astra", time.Hour)
		i := slices.IndexFunc(r.allCandidates(4000, r.Objective()), func(c candidate) bool { return c.key() == "openai-codex/gpt-6-sol" })
		in := BriefInput{ContextTokens: 4000}
		prev := r.briefDecision(in, BriefClass{Source: "test"}, 0.85, "test", []candidate{r.allCandidates(4000, r.Objective())[i]})
		next := r.EscalateBrief(prev, in)
		switch {
		case o == ObjectiveAuto && (next == nil || next.Tier != "paid"):
			t.Fatalf("auto escalates past the subscription to paid, got %+v", next)
		case o == ObjectiveCost && next != nil:
			t.Fatalf("cost escalation must stop at the subscription, got %s", next.Spec())
		}
	}
	// Short of the cap, cost still escalates, strictly up.
	r := costRouter(t)
	d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "quick", Brief: "x", ContextTokens: 4000})
	for next := r.EscalateBrief(d, BriefInput{ContextTokens: 4000}); next != nil; next = r.EscalateBrief(next, BriefInput{ContextTokens: 4000}) {
		if next.Tier == "paid" || r.capabilityOf(next.chosen) <= r.capabilityOf(d.chosen) {
			t.Fatalf("cost escalation from %s went to %s", d.Spec(), next.Spec())
		}
		d = next
	}
	if d.Spec() != "anthropic/claude-opus-5-5" {
		t.Fatalf("cost escalation should end on the strongest subscription model, got %s", d.Spec())
	}
}

func TestCostPrefersTheSubscriptionWithTheMostQuotaLeft(t *testing.T) {
	r := costRouter(t)
	now := time.Now()
	usage := map[string][]ai.UsageWindow{}
	r.usage = func() []ai.PlanUsage {
		var out []ai.PlanUsage
		for plan, windows := range usage {
			out = append(out, ai.PlanUsage{Plan: plan, Windows: windows})
		}
		return out
	}
	first := func() string {
		chain := r.rankBrief(BriefInput{ContextTokens: 4000}, BriefClass{}, 0.8, "")
		return chain[0].key()
	}
	// ChatGPT near its limit, Claude unreported: Claude.
	usage[ai.SubscriptionChatGPT] = []ai.UsageWindow{{Label: "5h", UsedPercent: 90, ResetsAt: now.Add(time.Hour)}}
	if got := first(); got != "anthropic/claude-sonnet-5" {
		t.Fatalf("ChatGPT at 90%%: want sonnet, got %s", got)
	}
	// Claude's weekly window near its limit: the least capable adequate
	// ChatGPT model.
	usage[ai.SubscriptionChatGPT] = []ai.UsageWindow{{Label: "5h", UsedPercent: 10}, {Label: "wk", UsedPercent: 5}}
	usage[ai.SubscriptionClaude] = []ai.UsageWindow{{Label: "5h", UsedPercent: 0}, {Label: "7d", UsedPercent: 80}}
	if got := first(); got != "openai-codex/gpt-6-sol" {
		t.Fatalf("Claude at 80%% weekly: want sol, got %s", got)
	}
	if d := r.decideWith(demanding(1, 1, 1), 4000); d == nil || d.Spec() != "openai-codex/gpt-6-sol" {
		t.Fatalf("the orchestrator should follow the quota too, got %+v", d)
	}
	// A window that has reset no longer counts.
	usage[ai.SubscriptionClaude] = []ai.UsageWindow{{Label: "5h", UsedPercent: 99, ResetsAt: now.Add(-time.Minute)}}
	if got := first(); got != "anthropic/claude-sonnet-5" {
		t.Fatalf("Claude's window reset: want sonnet, got %s", got)
	}
}

func TestCostBriefBarsNeedCertaintyToStepUp(t *testing.T) {
	for _, tc := range []struct {
		p          map[string]float64
		auto, cost float64
	}{
		{map[string]float64{"mechanical": 0.6, "routine": 0.4}, 0.6, 0.4},
		{map[string]float64{"mechanical": 0.7, "complex": 0.3}, 0.8, 0.4},
		{map[string]float64{"routine": 0.2, "complex": 0.8}, 0.8, 0.8},
	} {
		auto, _ := briefNeed(BriefClass{Difficulty: tc.p}, ObjectiveAuto)
		cost, _ := briefNeed(BriefClass{Difficulty: tc.p}, ObjectiveCost)
		if auto != tc.auto || cost != tc.cost {
			t.Errorf("%v: auto %.2f cost %.2f, want %.2f and %.2f", tc.p, auto, cost, tc.auto, tc.cost)
		}
	}
}

func TestCostStillAsksJevAndFallsBackCheap(t *testing.T) {
	jev := &fakeJev{reply: briefReply(map[string]float64{"routine": 0.5, "complex": 0.5}, "general")}
	r := jevRouter(t, jev)
	in := BriefInput{Type: "explore", Effort: "medium", Brief: "trace the config loader", ContextTokens: 4000}
	if d := r.DecideBrief(context.Background(), in); d == nil || d.need != 0.8 {
		t.Fatalf("auto: want need 0.8, got %+v", d)
	}
	if err := setObjectives(r, ObjectiveCost); err != nil {
		t.Fatal(err)
	}
	if d := r.DecideBrief(context.Background(), in); d == nil || d.need != 0.6 || d.Brief.Source != "jev" {
		t.Fatalf("cost: want need 0.6 from jev, got %+v", d)
	}
	if n := len(jev.requests()); n != 2 {
		t.Fatalf("jev calls %d, want 2", n)
	}
	jev.mu.Lock()
	jev.status = 500
	jev.mu.Unlock()
	if d := r.DecideBrief(context.Background(), in); d == nil || !d.Basic || r.cfg.Tiers[d.chosen.tier].Cost != CostFreeLocal {
		t.Fatalf("cost: a jev failure should route by the Basic rules, local first, got %+v", d)
	}
}

func TestCostIsAlwaysOfferedSecondAndCanStartFromTheEnv(t *testing.T) {
	cfg := testConfig()
	for i := range cfg.Tiers {
		for j := range cfg.Tiers[i].Models {
			cfg.Tiers[i].Models[j].Uncensored = false
		}
	}
	r := newTestRouter(t, withJev(cfg), testHost())
	if got := r.Objectives(); !slices.Equal(got, []Objective{ObjectiveAuto, ObjectiveCost, ObjectiveSpeed, ObjectiveQuality}) {
		t.Fatalf("objectives %v", got)
	}
	t.Setenv("WOPR_ROUTER", "cost")
	cfg, err := Load(t.TempDir())
	if err != nil || !cfg.Enabled {
		t.Fatalf("WOPR_ROUTER=cost: enabled %v, err %v", cfg.Enabled, err)
	}
	if r := newTestRouter(t, withJev(cfg), testHost()); r.Objective() != ObjectiveCost || r.Mode() != ModeAuto {
		t.Fatalf("objective %s mode %s, want cost and auto", r.Objective(), r.Mode())
	}
}

// Jev going down in cost mode once sent a turn to the user's leftover
// pay-per-token model; the Basic rules must keep cost mode unpaid.
func TestJevDownInCostModeNeverPays(t *testing.T) {
	jev := promptJev()
	jev.status = http.StatusServiceUnavailable
	r := jevRouter(t, jev)
	if err := setObjectives(r, ObjectiveCost); err != nil {
		t.Fatal(err)
	}
	// Too big for the owned models: the subscription is the ceiling.
	d := r.Decide(context.Background(), Input{Prompt: "fix it", ContextTokens: 200_000})
	if d == nil || !d.Basic || r.cfg.Tiers[d.chosen.tier].Cost != CostSubscription {
		t.Fatalf("want a subscription model by the Basic rules, got %+v", d)
	}
	for _, ref := range r.cfg.Tiers[2].Models {
		r.markDown(ref.Spec(), time.Hour)
	}
	r.MarkCold()
	if d := r.Decide(context.Background(), Input{Prompt: "fix it", ContextTokens: 200_000}); d != nil {
		t.Fatalf("nothing unpaid is eligible: want no route, got %s", d.Spec())
	}
	if why := r.NoRouteReason(200_000, ObjectiveCost); !strings.Contains(why, "cost mode never uses") {
		t.Fatalf("the reason should say why: %q", why)
	}
}

func TestQuotaBalanceMovesToARoomierPeerPlan(t *testing.T) {
	cfg := testConfig()
	cfg.Engine = EngineBasic
	r := newTestRouter(t, cfg, testHost())
	now := time.Now()
	claudeUsed := 0.0
	r.usage = func() []ai.PlanUsage {
		return []ai.PlanUsage{
			{Plan: ai.SubscriptionClaude, Windows: []ai.UsageWindow{{Label: "5h", UsedPercent: claudeUsed, ResetsAt: now.Add(4 * time.Hour)}}},
			{Plan: ai.SubscriptionChatGPT, Windows: []ai.UsageWindow{{Label: "5h", UsedPercent: 20, ResetsAt: now.Add(time.Hour)}}},
		}
	}
	decide := func() *Decision {
		r.MarkCold()
		return r.Decide(context.Background(), Input{Prompt: "fix it", ContextTokens: 200_000})
	}
	// Claude has room: the top-ranked model stays.
	if d := decide(); d == nil || d.Spec() != "anthropic/claude-opus-5-5" {
		t.Fatalf("Claude not tight: want opus, got %+v", d)
	}
	// Claude tight, ChatGPT roomy: its peer one rank step down takes over.
	claudeUsed = 85
	d := decide()
	if d == nil || d.Spec() != "openai-codex/gpt-6-astra" || !strings.Contains(d.Reason, "quota: Claude 5h 85% used") {
		t.Fatalf("Claude tight: want astra with a quota reason, got %+v", d)
	}
	// Off: the ranking wins again.
	r.SetQuotaBalance(false)
	if d := decide(); d == nil || d.Spec() != "anthropic/claude-opus-5-5" {
		t.Fatalf("balancing off: want opus, got %+v", d)
	}
}

// Private mode never lets data leave the Privacy Safe models: not the
// orchestrator, a brief, or a side task, and not by way of Jev unless Jev is
// marked Privacy Safe too.
func TestPrivateModeStaysOnPrivacySafeModels(t *testing.T) {
	private := func(jevSafe bool) (*Router, *fakeJev) {
		cfg := testConfig()
		cfg.Tiers[0].Models[0].PrivacySafe = true
		cfg.Tiers[1].Models[0].PrivacySafe = true
		jev := promptJev()
		cfg = withFakeJev(t, cfg, jev)
		cfg.Jev.PrivacySafe = jevSafe
		r := newTestRouter(t, cfg, testHost())
		teachSpeeds(r)
		if err := setObjectives(r, ObjectivePrivate); err != nil {
			t.Fatal(err)
		}
		return r, jev
	}
	brief := BriefInput{Type: "explore", Brief: "trace how the session saves", ContextTokens: 8000}

	r, jev := private(false)
	if d := r.Decide(context.Background(), Input{Prompt: "fix it", ContextTokens: 8000}); d == nil || !r.IsPrivacySafe(d.Spec()) {
		t.Fatalf("orchestrator: want a Privacy Safe model, got %+v", d)
	}
	if d := r.DecideBrief(context.Background(), brief); d == nil || !r.IsPrivacySafe(d.Spec()) {
		t.Fatalf("brief: want a Privacy Safe model, got %+v", d)
	}
	if ref, _, ok := r.SideTask(8000); !ok || !ref.PrivacySafe {
		t.Fatalf("side task: want a Privacy Safe model, got %+v %v", ref, ok)
	}
	if n := len(jev.requests()); n != 0 {
		t.Fatalf("Jev isn't Privacy Safe but was asked %d times", n)
	}

	// The Privacy Safe models go down: nothing else may take the work.
	for _, idx := range []int{0, 1} {
		r.markDown(r.cfg.Tiers[idx].Models[0].Spec(), time.Hour)
	}
	r.MarkCold()
	if d := r.Decide(context.Background(), Input{Prompt: "fix it", ContextTokens: 8000}); d != nil {
		t.Fatalf("orchestrator: want no route, got %s", d.Spec())
	}
	if d := r.DecideBrief(context.Background(), brief); d != nil {
		t.Fatalf("brief: want no route, got %s", d.Spec())
	}
	if ref, _, ok := r.SideTask(8000); ok {
		t.Fatalf("side task: want none, got %s", ref.Spec())
	}
	if why := r.NoRouteReason(8000, ObjectivePrivate); !strings.Contains(why, "not marked Privacy Safe") {
		t.Fatalf("the reason should say why: %q", why)
	}

	// Jev marked Privacy Safe is asked, and the pick still stays private.
	r, jev = private(true)
	if d := r.Decide(context.Background(), Input{Prompt: "fix it", ContextTokens: 8000}); d == nil || !r.IsPrivacySafe(d.Spec()) {
		t.Fatalf("with a Privacy Safe Jev: want a Privacy Safe model, got %+v", d)
	}
	if len(jev.requests()) == 0 {
		t.Fatal("Jev is Privacy Safe but wasn't asked")
	}
}
