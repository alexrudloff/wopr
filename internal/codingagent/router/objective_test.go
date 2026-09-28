package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWOPRRouterEnvStartsInAMode(t *testing.T) {
	t.Setenv("WOPR_ROUTER", "speed")
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Fatal("WOPR_ROUTER=speed must turn routing on without a router.json")
	}
	if r := newTestRouter(t, cfg, testHost()); r.Mode() != ModeAuto || r.Objective() != ObjectiveSpeed || r.Engine() != EngineBasic {
		t.Fatalf("without Jev the Basic rules route, got mode %s, engine %s", r.Mode(), r.Engine())
	}
	r := newTestRouter(t, withJev(cfg), testHost())
	if r.Objective() != ObjectiveSpeed || r.Mode() != ModeAuto {
		t.Fatalf("objective %s mode %s, want speed and auto", r.Objective(), r.Mode())
	}

	t.Setenv("WOPR_ROUTER", "off")
	if cfg, err := Load(t.TempDir()); err != nil || cfg.Enabled {
		t.Fatalf("WOPR_ROUTER=off: enabled %v, err %v", cfg.Enabled, err)
	}

	t.Setenv("WOPR_ROUTER", "uncensored")
	dir := t.TempDir()
	config := `{"enabled":true,"tiers":[{"name":"local","cost":"free-local","capability":0.5,"models":[{"provider":"p","model":"m"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("WOPR_ROUTER=uncensored must fail when no model is flagged uncensored")
	}
}

// setObjectives puts the orchestrator and the subagents in mode o.
func setObjectives(r *Router, o Objective) error {
	if err := r.SetObjective(o); err != nil {
		return err
	}
	return r.SetSubagentObjective(o)
}

func objectiveRouter(t *testing.T, o Objective) *Router {
	t.Helper()
	r := briefRouter(t)
	if err := setObjectives(r, o); err != nil {
		t.Fatal(err)
	}
	return r
}

// needs returns an implement classification whose demand is need.
func needs(need float64) Classification {
	v := need / 1.05
	return Classification{Kind: KindImplement, Complexity: v, Capability: v, DeepReasoning: v, Source: "test"}
}

func TestSpeedPicksTheFastestAdequateModel(t *testing.T) {
	r := objectiveRouter(t, ObjectiveSpeed)
	r.speed.stats["anthropic/claude-sonnet-5"] = SpeedStat{BaseSeconds: 6, Samples: 3}
	r.speed.stats["anthropic/claude-opus-5-5"] = SpeedStat{BaseSeconds: 1.5, Samples: 3}
	// Faster, but too weak or pay-per-token.
	r.speed.stats["lan/open-model"] = SpeedStat{BaseSeconds: 0.2, Samples: 3}
	r.speed.stats["openrouter/anthropic/claude-opus-5.5"] = SpeedStat{BaseSeconds: 0.1, Samples: 3}
	d := r.decideWith(needs(0.8), 4000)
	if d == nil || d.Spec() != "anthropic/claude-opus-5-5" || d.Objective != ObjectiveSpeed {
		t.Fatalf("speed should take the fastest adequate model, got %+v", d)
	}
	// An unmeasured model never beats a measured one.
	delete(r.speed.stats, "anthropic/claude-opus-5-5")
	if d := r.decideWith(needs(0.8), 4000); d == nil || d.Spec() != "anthropic/claude-sonnet-5" {
		t.Fatalf("unmeasured models must count as slow, got %+v", d)
	}
}

func TestSpeedBriefIgnoresCostClassButKeepsTheFloor(t *testing.T) {
	r := objectiveRouter(t, ObjectiveSpeed)
	r.speed.stats["openai-codex/gpt-6-luna"] = SpeedStat{BaseSeconds: 2, Samples: 3}
	class := BriefClass{Difficulty: map[string]float64{"routine": 1}}
	// the local model is adequate and free but ~27s cold at 8K; the LAN model are fast but
	// too weak for a routine brief.
	got := r.rankBrief(BriefInput{ContextTokens: 8000}, class, 0.6, "")
	if len(got) == 0 || got[0].key() != "openai-codex/gpt-6-luna" {
		t.Fatalf("speed brief should take luna, got %v", specs(got))
	}
	if err := setObjectives(r, ObjectiveAuto); err != nil {
		t.Fatal(err)
	}
	if got := r.rankBrief(BriefInput{ContextTokens: 8000}, class, 0.6, ""); got[0].ref.Provider != "local" {
		t.Fatalf("auto keeps free first, got %v", specs(got))
	}
}

func TestObjectiveShiftsThinking(t *testing.T) {
	class := needs(0.8)
	class.Complexity, class.Capability = 0.55, 0.55 // demand 0.5+: medium in auto
	levels := map[Objective]int{}
	for _, o := range []Objective{ObjectiveAuto, ObjectiveCost, ObjectiveSpeed, ObjectiveQuality} {
		r := briefRouter(t)
		c := candidate{tier: 2, ref: r.cfg.Tiers[2].Models[4], info: testHost().models["anthropic/claude-opus-5-5"]}
		levels[o] = rank(r.thinkingFor(class, class.Demand(), r.cfg.Tiers[2], c, o))
	}
	if levels[ObjectiveSpeed] != levels[ObjectiveAuto]-1 || levels[ObjectiveCost] != levels[ObjectiveAuto]-1 || levels[ObjectiveQuality] != levels[ObjectiveAuto]+1 {
		t.Fatalf("thinking ranks %v: speed and cost one below auto, quality one above", levels)
	}
}

func TestQualityBandIncludesAStrongLocalModel(t *testing.T) {
	cfg := testConfig()
	cfg.Enabled = true
	cfg.Tiers[0].Models[0].Capability = 0.98
	r := newTestRouter(t, cfg, testHost())
	if err := setObjectives(r, ObjectiveQuality); err != nil {
		t.Fatal(err)
	}
	band := r.band(r.allCandidates(4000, r.Objective()), nil)
	want := map[string]bool{"local/deepseek-v4-flash": true, "openai-codex/gpt-6-astra": true, "anthropic/claude-opus-5-5": true}
	if len(band) != len(want) {
		t.Fatalf("band %v, want %v", specs(band), want)
	}
	for _, c := range band {
		if !want[c.key()] {
			t.Fatalf("band %v has %s", specs(band), c.key())
		}
	}
	// Chat still goes to the band, cost ignored: the fastest member.
	r.speed.stats["anthropic/claude-opus-5-5"] = SpeedStat{BaseSeconds: 1, Samples: 3}
	if d := r.decideWith(chat(), 4000); d == nil || d.Spec() != "anthropic/claude-opus-5-5" {
		t.Fatalf("quality chat should use the band, got %+v", d)
	}
}

func TestQualityBandSlidesWhenTheBestIsUnavailable(t *testing.T) {
	r := objectiveRouter(t, ObjectiveQuality)
	d := r.decideWith(chat(), 4000)
	if d == nil || r.capabilityOf(d.chosen) < 0.95 {
		t.Fatalf("quality should pick opus or astra, got %+v", d)
	}
	r.markDown("anthropic/claude-opus-5-5", time.Hour)
	r.markDown("openai-codex/gpt-6-astra", time.Hour)
	d = r.decideWith(chat(), 4000)
	if d == nil || (d.Spec() != "openai-codex/gpt-6-sol" && d.Spec() != "anthropic/claude-sonnet-5") {
		t.Fatalf("band should slide to sol and sonnet, got %+v", d)
	}
	// Rate-limited out of the band's top, failover stays in the new band.
	next := r.Next(d, Failure{Message: "429", RateLimited: true})
	if next == nil || r.capabilityOf(next.chosen) < 0.8 || next.Tier == "paid" {
		t.Fatalf("quality failover should slide within the band, got %+v", next)
	}
	band := r.band(r.allCandidates(4000, r.Objective()), func(p string) bool { return true })
	if len(band) == 0 {
		t.Fatal("saturation alone must not empty the band")
	}
}

func TestQualityBriefUsesTheBandAndItsEscalation(t *testing.T) {
	r := objectiveRouter(t, ObjectiveQuality)
	d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "quick", Brief: "list callers", ContextTokens: 4000})
	if d == nil || r.capabilityOf(d.chosen) < 0.95 {
		t.Fatalf("quality brief should use the band, got %+v", d)
	}
	sat := func(p string) bool { return p == d.Provider }
	d2 := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "quick", Brief: "list callers", ContextTokens: 4000, Saturated: sat})
	if d2 == nil || d2.Provider == d.Provider || r.capabilityOf(d2.chosen) < 0.95 {
		t.Fatalf("a saturated provider should yield to another band member, got %+v", d2)
	}
}

func TestUncensoredNeverLeavesFlaggedModels(t *testing.T) {
	jev := promptJev()
	r := jevRouter(t, jev)
	if err := setObjectives(r, ObjectiveUncensored); err != nil {
		t.Fatal(err)
	}
	flagged := func(d *Decision) bool { return d == nil || d.Spec() == "lan/open-model" }
	d := r.Decide(context.Background(), Input{Prompt: "design a lock-free scheduler and prove it correct", ContextTokens: 4000})
	if d == nil || !flagged(d) || d.Objective != ObjectiveUncensored {
		t.Fatalf("orchestrator must be flagged, got %+v", d)
	}
	if next := r.Next(d, Failure{Message: "boom"}); !flagged(next) {
		t.Fatalf("failover left the flagged models: %+v", next)
	}
	delete(r.down, "lan/open-model")
	b := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "thorough", Brief: "is this lock ordering safe?", ContextTokens: 4000})
	if b == nil || !flagged(b) {
		t.Fatalf("brief must be flagged, got %+v", b)
	}
	if next := r.EscalateBrief(b, BriefInput{ContextTokens: 4000}); next != nil {
		t.Fatalf("escalation left the flagged models: %s", next.Spec())
	}
	if ref, _, ok := r.SideTask(4000); !ok || ref.Spec() != "lan/open-model" {
		t.Fatalf("side task must be flagged, got %v %v", ref, ok)
	}
	// Unflagged decisions from before the switch cannot fail over out.
	prev := &Decision{Provider: "anthropic", Model: "claude-opus-5-5", Tier: "subscription", chain: r.rankBrief(BriefInput{ContextTokens: 4000}, BriefClass{}, 0, "")}
	if next := r.Next(prev, Failure{Message: "boom"}); !flagged(next) {
		t.Fatalf("stale chain left the flagged models: %+v", next)
	}
	delete(r.down, "anthropic/claude-opus-5-5")
	r.markDown("lan/open-model", time.Hour)
	if d := r.decideWith(needs(0.9), 4000); d != nil {
		t.Fatalf("nothing flagged is reachable: want no route, got %s", d.Spec())
	}
	if b := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "medium", Brief: "x", ContextTokens: 4000}); b != nil {
		t.Fatalf("nothing flagged is reachable: want no brief route, got %s", b.Spec())
	}
}

func TestUncensoredIsOfferedOnlyWhenAModelIsFlagged(t *testing.T) {
	r := briefRouter(t)
	if got := r.Objectives(); len(got) != 5 || got[4] != ObjectiveUncensored {
		t.Fatalf("default config flags the LAN model: %v", got)
	}
	cfg := testConfig()
	for i := range cfg.Tiers {
		for j := range cfg.Tiers[i].Models {
			cfg.Tiers[i].Models[j].Uncensored = false
		}
	}
	r = newTestRouter(t, withJev(cfg), testHost())
	if got := r.Objectives(); len(got) != 4 {
		t.Fatalf("no flagged model: %v", got)
	}
	if err := r.SetObjective(ObjectiveUncensored); err == nil {
		t.Fatal("uncensored must be refused without a flagged model")
	}
}

func TestOrchestratorAndSubagentModesAreSeparate(t *testing.T) {
	r := briefRouter(t)
	r.SetMode(ModeOff)
	if err := r.SetObjective(ObjectiveSpeed); err != nil || !r.Auto() || r.SubagentRouting() {
		t.Fatalf("an orchestrator mode routes the orchestrator only: %v auto %v subagents %v", err, r.Auto(), r.SubagentRouting())
	}
	if err := r.SetSubagentObjective(ObjectiveQuality); err != nil || !r.SubagentRouting() || r.Objective() != ObjectiveSpeed {
		t.Fatalf("a subagent mode leaves the orchestrator's: %v %s", err, r.Objective())
	}
	r.PinOrchestrator()
	if r.Auto() || r.SubagentObjective() != ObjectiveQuality {
		t.Fatalf("picking a model keeps the subagents' mode, got %s", r.SubagentObjective())
	}
	r.SetMode(ModeOff)
	if r.Objective() != ObjectiveAuto || r.SubagentObjective() != ObjectiveAuto {
		t.Fatalf("off resets the modes, got %s and %s", r.Objective(), r.SubagentObjective())
	}
}
