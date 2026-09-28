package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

type fakeHost struct {
	models map[string]ModelInfo
}

func (h fakeHost) ModelInfo(provider, model string) (ModelInfo, bool) {
	info, ok := h.models[provider+"/"+model]
	return info, ok
}

func (h fakeHost) APIKey(string) string { return "" }

// testConfig is a configuration like one /setup writes: a slow local
// model, a fast one on the network, subscription models, and pay-per-token
// ones, routing on with a classifier that is never called unless a test
// swaps in a fake one, and no probes (no network in tests).
func testConfig() Config {
	cfg := withJev(DefaultConfig())
	claude := map[string]float64{"frontend_ui": 0.10, "docs_prose": 0.05}
	codex := map[string]float64{"backend_api": 0.05, "data_sql_pipelines": 0.10, "infra_devops_build": 0.05}
	cfg.Tiers = []TierConfig{
		{Name: "local", Cost: CostFreeLocal, Models: []ModelRef{{Provider: "local", Model: "deepseek-v4-flash", Capability: 0.6}}},
		{Name: "lan", Cost: CostFreeRemote, Models: []ModelRef{{Provider: "lan", Model: "open-model", Capability: 0.45, Uncensored: true}}},
		{Name: "subscription", Cost: CostSubscription, Models: []ModelRef{
			{Provider: "openai-codex", Model: "gpt-6-luna", Capability: 0.7, Affinity: codex},
			{Provider: "anthropic", Model: "claude-sonnet-5", Capability: 0.8, Affinity: claude},
			{Provider: "openai-codex", Model: "gpt-6-sol", Capability: 0.85, Affinity: codex},
			{Provider: "openai-codex", Model: "gpt-6-astra", Capability: 0.95, Affinity: codex},
			{Provider: "anthropic", Model: "claude-opus-5-5", Capability: 1.0, Affinity: claude},
		}},
		{Name: "paid", Cost: CostPaid, Models: []ModelRef{
			{Provider: "openrouter", Model: "deepseek/deepseek-v4-flash", Capability: 0.7},
			{Provider: "openrouter", Model: "anthropic/claude-opus-5.5", Capability: 1.0},
		}},
	}
	cfg.Subagents.ProviderParallel = map[string]int{"local": 1, "anthropic": 2, "openai-codex": 2}
	return cfg
}

func testHost() fakeHost {
	return fakeHost{models: map[string]ModelInfo{
		"local/deepseek-v4-flash":               {DisplayName: "local", ContextWindow: 65536, MaxOutputTokens: 16000, MaxThinking: ai.ThinkingHigh},
		"lan/open-model":                        {DisplayName: "lan", ContextWindow: 131072, MaxOutputTokens: 16384, MaxThinking: ai.ThinkingHigh},
		"openai-codex/gpt-6-luna":               {DisplayName: "luna", ContextWindow: 272000, MaxOutputTokens: 128000, MaxThinking: ai.ThinkingXHigh},
		"anthropic/claude-sonnet-5":             {DisplayName: "sonnet", ContextWindow: 1_000_000, MaxOutputTokens: 128000, MaxThinking: ai.ThinkingXHigh},
		"openai-codex/gpt-6-sol":                {DisplayName: "sol", ContextWindow: 272000, MaxOutputTokens: 128000, MaxThinking: ai.ThinkingXHigh},
		"openai-codex/gpt-6-astra":              {DisplayName: "astra", ContextWindow: 272000, MaxOutputTokens: 128000, MaxThinking: ai.ThinkingXHigh},
		"anthropic/claude-opus-5-5":             {DisplayName: "opus", ContextWindow: 1_000_000, MaxOutputTokens: 128000, MaxThinking: ai.ThinkingXHigh},
		"openrouter/deepseek/deepseek-v4-flash": {DisplayName: "paid flash", ContextWindow: 1_000_000, MaxOutputTokens: 384000, MaxThinking: ai.ThinkingHigh},
		"openrouter/anthropic/claude-opus-5.5":  {DisplayName: "paid opus", ContextWindow: 1_000_000, MaxOutputTokens: 128000, MaxThinking: ai.ThinkingXHigh},
	}}
}

func newTestRouter(t *testing.T, cfg Config, host Host) *Router {
	t.Helper()
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	return New(cfg, host)
}

// teachSpeeds gives the local model a slow prefill and the LAN model a fast one, as
// a laptop and a GPU box measure.
func teachSpeeds(r *Router) {
	r.speed.stats["local/deepseek-v4-flash"] = SpeedStat{BaseSeconds: 8.5, PerKSeconds: 2.4, Samples: 4}
	r.speed.stats["lan/open-model"] = SpeedStat{BaseSeconds: 1.0, PerKSeconds: 0.4, Samples: 4}
}

func chat() Classification {
	return Classification{Kind: KindChat, Complexity: 0.05, Capability: 0.05, DeepReasoning: 0.1, Source: "test"}
}

func TestChatGoesToTheFastestAdequateFreeModel(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	// Unmeasured: equal speed, so the stronger free model wins.
	if d := r.decideWith(chat(), 4000); d == nil || d.Tier != "local" || d.Thinking != ai.ThinkingOff {
		t.Fatalf("unmeasured chat: %+v", d)
	}
	teachSpeeds(r)
	if d := r.decideWith(chat(), 4000); d == nil || d.Tier != "lan" {
		t.Fatalf("measured chat should take the fast cluster, got %+v", d)
	}
}

func TestFastButIncapableNeverBeatsCapable(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	teachSpeeds(r)
	// demand 0.5*0.6 + 0.4*0.6 + 0.15*0.3 = 0.585: Qwen (0.45) is not
	// adequate however fast it is; the local model (0.6) is.
	d := r.decideWith(Classification{Kind: KindImplement, Complexity: 0.6, Capability: 0.6, DeepReasoning: 0.3, Source: "test"}, 4000)
	if d == nil || d.Tier != "local" {
		t.Fatalf("expected the capable slow model, got %+v", d)
	}
}

func TestStickinessFollowsTheWarmCache(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	r.speed.stats["local/deepseek-v4-flash"] = SpeedStat{BaseSeconds: 0.5, PerKSeconds: 2.4, Samples: 4}
	r.speed.stats["lan/open-model"] = SpeedStat{BaseSeconds: 1.0, PerKSeconds: 0.4, Samples: 4}
	// A cold 20K context: the LAN model prefill it faster.
	if d := r.decideWith(chat(), 20000); d.Tier != "lan" {
		t.Fatalf("cold: expected cluster, got %s", d.Tier)
	}
	// the local model just served 19.5K of it and holds that prefix: staying is faster.
	r.Observe("local/deepseek-v4-flash", 19500, 0, 0)
	if d := r.decideWith(chat(), 20000); d.Tier != "local" {
		t.Fatalf("warm: expected to stay on local, got %s", d.Tier)
	}
}

func TestPlanUsesAFrontierSubscriptionModel(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	d := r.decideWith(Classification{Kind: KindPlan, Complexity: 0.4, Capability: 0.4, DeepReasoning: 0.8, Source: "test"}, 2000)
	if d == nil || d.Tier != "subscription" || d.Model != "claude-opus-5-5" {
		t.Fatalf("unmeasured plan should take the strongest subscription model, got %+v", d)
	}
	if rank(d.Thinking) < rank(ai.ThinkingMedium) {
		t.Fatalf("expected boosted thinking for plan, got %s", d.Thinking)
	}
	// Once measured, the fastest frontier-class model wins; Luna and Sonnet
	// stay below the plan floor.
	r.speed.stats["openai-codex/gpt-6-sol"] = SpeedStat{BaseSeconds: 1, Samples: 1}
	r.speed.stats["anthropic/claude-opus-5-5"] = SpeedStat{BaseSeconds: 3, Samples: 1}
	r.speed.stats["openai-codex/gpt-6-astra"] = SpeedStat{BaseSeconds: 4, Samples: 1}
	r.speed.stats["anthropic/claude-sonnet-5"] = SpeedStat{BaseSeconds: 3, Samples: 1}
	r.speed.stats["openai-codex/gpt-6-luna"] = SpeedStat{BaseSeconds: 0.5, Samples: 1}
	if d := r.decideWith(Classification{Kind: KindPlan, DeepReasoning: 0.8, Source: "test"}, 2000); d.Model != "gpt-6-sol" {
		t.Fatalf("expected the fastest frontier model, got %s", d.Model)
	}
}

func TestHighDemandLeavesFreeTiers(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	d := r.decideWith(Classification{Kind: KindImplement, Complexity: 0.9, Capability: 0.9, DeepReasoning: 0.9, Source: "test"}, 2000)
	if d == nil || d.Tier != "subscription" {
		t.Fatalf("expected the subscription tier, got %+v", d)
	}
}

func TestContextTooBigSkipsLocal(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	d := r.decideWith(Classification{Kind: KindImplement, Complexity: 0.3, Capability: 0.3, Source: "test"}, 60000)
	if d == nil || d.Tier != "lan" {
		t.Fatalf("expected cluster when 60K tokens do not fit local, got %+v", d)
	}
}

func TestPayOnlyWhenNeeded(t *testing.T) {
	host := testHost()
	for _, m := range []string{"openai-codex/gpt-6-luna", "anthropic/claude-sonnet-5", "openai-codex/gpt-6-sol", "openai-codex/gpt-6-astra", "anthropic/claude-opus-5-5"} {
		delete(host.models, m)
	}
	r := newTestRouter(t, testConfig(), host)
	// A light review with no subscription: the floor is a preference, so a
	// free model that meets the actual demand wins over paying.
	d := r.decideWith(Classification{Kind: KindReview, Complexity: 0.3, Capability: 0.3, DeepReasoning: 0.2, Source: "test"}, 2000)
	if d == nil || d.Tier == "paid" || !strings.Contains(d.Reason, "floor") {
		t.Fatalf("light review should stay free, got %+v", d)
	}
	// Nothing free fits 500K tokens: paid as last resort.
	d = r.decideWith(Classification{Kind: KindImplement, Complexity: 0.3, Capability: 0.3, Source: "test"}, 500000)
	if d == nil || d.Tier != "paid" {
		t.Fatalf("expected paid last resort for a huge context, got %+v", d)
	}
	cfg := testConfig()
	cfg.PaidLastResort = false
	r = newTestRouter(t, cfg, host)
	if d := r.decideWith(Classification{Kind: KindImplement, Source: "test"}, 500000); d != nil {
		t.Fatalf("expected no route when paid is forbidden and nothing fits, got %+v", d)
	}
}

func TestNextFallsThroughAndRests(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	d := r.decideWith(chat(), 2000)
	if d.Tier != "local" {
		t.Fatalf("setup: expected local, got %s", d.Tier)
	}
	next := r.Next(d, Failure{Message: "connection refused", ContextTokens: 2000})
	if next == nil || next.Tier != "lan" || !next.Fallback {
		t.Fatalf("expected cluster fallback, got %+v", next)
	}
	if again := r.decideWith(chat(), 2000); again.Tier != "lan" {
		t.Fatalf("expected local to be resting, got %s", again.Tier)
	}
	now = now.Add(2 * time.Minute)
	if again := r.decideWith(chat(), 2000); again.Tier != "local" {
		t.Fatalf("expected local back after cooldown, got %s", again.Tier)
	}
}

func TestOverflowNeedsBiggerWindow(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	d := r.decideWith(chat(), 2000)
	next := r.Next(d, Failure{Message: "context length exceeded", Overflow: true, ContextTokens: 70000})
	if next == nil || next.Tier != "lan" {
		t.Fatalf("expected the next larger window, got %+v", next)
	}
	if again := r.decideWith(chat(), 2000); again.Tier != "local" {
		t.Fatalf("expected local usable after overflow, got %s", again.Tier)
	}
}

func TestRateLimitRestsTheModel(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	now := time.Now()
	r.now = func() time.Time { return now }
	d := r.decideWith(Classification{Kind: KindPlan, DeepReasoning: 0.9, Source: "test"}, 2000)
	if d.Model != "claude-opus-5-5" {
		t.Fatalf("setup: expected opus, got %s", d.Model)
	}
	next := r.Next(d, Failure{Message: "HTTP 429: usage limit reached", RateLimited: true, ContextTokens: 2000})
	if next == nil || next.Model != "gpt-6-astra" {
		t.Fatalf("expected the next frontier model, got %+v", next)
	}
	now = now.Add(10 * time.Minute)
	if !r.isDown("anthropic/claude-opus-5-5") {
		t.Fatal("expected opus to rest for the quota backoff")
	}
}

func TestPinForcesTier(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	if err := r.Pin("lan"); err != nil {
		t.Fatal(err)
	}
	d := r.decideWith(Classification{Kind: KindPlan, DeepReasoning: 0.9, Source: "test"}, 2000)
	if d == nil || d.Tier != "lan" {
		t.Fatalf("expected pinned cluster, got %+v", d)
	}
	if err := r.Pin("nope"); err == nil {
		t.Fatal("expected error for unknown tier")
	}
}

func TestSideTaskPrefersCheapestWeakestThatFits(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	if ref, _, ok := r.SideTask(3000); !ok || ref.Provider != "lan" {
		t.Fatalf("expected the weakest free model for a small side task, got %+v", ref)
	}
	// 120K fits neither free model: the weakest subscription model, not Opus.
	if ref, _, ok := r.SideTask(120000); !ok || ref.Model != "gpt-6-luna" {
		t.Fatalf("expected luna for a 120K summary, got %+v", ref)
	}
}

func TestSpeedLearning(t *testing.T) {
	book := loadSpeedBook(t.TempDir())
	// Two short requests pin the overhead, one long one the prefill rate.
	book.observe("m", 200, 2*time.Second)
	book.observe("m", 5000, 14*time.Second)
	got, ok := book.estimate("m", 5000)
	if !ok || got < 5 || got > 15 {
		t.Fatalf("estimate %v %v", got, ok)
	}
	if _, ok := book.estimate("other", 100); ok {
		t.Fatal("unmeasured model must report unknown")
	}
	reloaded := loadSpeedBook(filepath.Dir(book.path))
	if _, ok := reloaded.estimate("m", 100); !ok {
		t.Fatal("speeds should persist")
	}
}

func TestJevAnswersParse(t *testing.T) {
	score := 2.4
	deep := 0.8
	sens := 0.05
	resp := jevResponse{Answers: map[string]jevAnswer{
		qKind:      {Type: "choice", Choice: "debug", Confidence: 0.7},
		qComplex:   {Type: "score", Score: &score},
		qCapable:   {Type: "score", Score: &score},
		qDeep:      {Type: "noul", Noul: &deep},
		qSensitive: {Type: "noul", Noul: &sens},
	}}
	resp.Usage.Cost = 0.00002
	class, err := classificationFromAnswers(resp)
	if err != nil {
		t.Fatal(err)
	}
	if class.Kind != KindDebug || class.Complexity != 0.6 || class.DeepReasoning != 0.8 || class.Source != "jev" {
		t.Fatalf("unexpected classification %+v", class)
	}
}

func TestTargetsGroupProvidersPerTier(t *testing.T) {
	r := newTestRouter(t, testConfig(), testHost())
	teachSpeeds(r)
	r.Observe("lan/open-model", 2000, 2000, time.Second)
	r.markDown("local/deepseek-v4-flash", time.Minute)
	got := r.Targets()
	want := []struct {
		tier, provider string
		health         int
		timed          bool
	}{
		{"local", "local", TargetDown, true},
		{"lan", "lan", TargetUp, true},
		{"subscription", "openai-codex", TargetUnknown, false},
		{"subscription", "anthropic", TargetUnknown, false},
		{"paid", "openrouter", TargetUnknown, false},
	}
	if len(got) != len(want) {
		t.Fatalf("Targets() = %+v", got)
	}
	for i, w := range want {
		if g := got[i]; g.Tier != w.tier || g.Provider != w.provider || g.Health != w.health || (g.TTFT > 0) != w.timed {
			t.Errorf("target %d = %+v, want %+v", i, g, w)
		}
	}
}
