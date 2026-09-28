package router

import (
	"context"
	"testing"
)

func TestBriefNeedUsesAsymmetricBars(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    map[string]float64
		want float64
	}{
		{"sure it is mechanical", map[string]float64{"mechanical": 0.75, "routine": 0.25}, 0.4},
		{"not sure enough to go cheap", map[string]float64{"mechanical": 0.6, "routine": 0.4}, 0.6},
		{"a little doubt stays cheap", map[string]float64{"mechanical": 0.75, "complex": 0.25}, 0.4},
		{"modest evidence goes up", map[string]float64{"mechanical": 0.7, "complex": 0.3}, 0.8},
		{"deep needs only 0.3", map[string]float64{"routine": 0.7, "deep": 0.3}, 0.95},
		{"no answer is the top", map[string]float64{}, 0.95},
	} {
		if got, _ := briefNeed(BriefClass{Difficulty: tc.p}, ObjectiveAuto); got != tc.want {
			t.Errorf("%s: need %.2f, want %.2f", tc.name, got, tc.want)
		}
	}
	if got, _ := briefNeed(BriefClass{Difficulty: map[string]float64{"mechanical": 1}, Risk: 0.6}, ObjectiveAuto); got != 0.85 {
		t.Errorf("risky brief need %.2f, want 0.85", got)
	}
	if got, _ := briefNeed(BriefClass{Difficulty: map[string]float64{"mechanical": 1}, Judgment: 0.6}, ObjectiveAuto); got != 0.7 {
		t.Errorf("judgment brief need %.2f, want 0.7", got)
	}
}

func briefRouter(t *testing.T) *Router {
	t.Helper()
	return jevRouter(t, promptJev())
}

func TestQuickBriefTakesTheFastestFreeModel(t *testing.T) {
	r := briefRouter(t)
	d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "quick", Brief: "list callers", ContextTokens: 8000})
	if d == nil || d.Tier != "lan" || d.Brief.Source != "rule" {
		t.Fatalf("quick explore should go to the LAN model by rule, got %+v", d)
	}
}

func TestLargeBriefAvoidsSlowPrefill(t *testing.T) {
	r := briefRouter(t)
	class := BriefClass{Difficulty: map[string]float64{"routine": 1}}
	// the local model prefill: 8.5s + 2.4s/K. 8K cold is ~28s, under the 30s limit;
	// 25K would take ~68s.
	small := r.rankBrief(BriefInput{ContextTokens: 8000}, class, 0.6, "")
	if len(small) == 0 || small[0].ref.Provider != "local" {
		t.Fatalf("a small routine brief may use the local model, got %v", specs(small))
	}
	large := r.rankBrief(BriefInput{ContextTokens: 25000}, class, 0.6, "")
	if len(large) == 0 || large[0].ref.Provider == "local" {
		t.Fatalf("a large brief must stay off the local model, got %v", specs(large))
	}
	if last := large[len(large)-1]; last.ref.Provider != "local" {
		t.Fatalf("the local model should only remain as the last resort, got %v", specs(large))
	}
}

func TestSaturatedProviderYieldsToTheNextModel(t *testing.T) {
	r := briefRouter(t)
	class := BriefClass{Difficulty: map[string]float64{"routine": 1}}
	busy := func(p string) bool { return p == "local" }
	chain := r.rankBrief(BriefInput{ContextTokens: 4000, Saturated: busy}, class, 0.6, "")
	if len(chain) == 0 || chain[0].ref.Provider == "local" {
		t.Fatalf("a busy the local model should yield, got %v", specs(chain))
	}
}

func TestBriefIsNotRoutedAboveTheOrchestrator(t *testing.T) {
	r := briefRouter(t)
	in := BriefInput{Type: "explore", Effort: "medium", Brief: "x", ContextTokens: 20000, Orchestrator: "local/deepseek-v4-flash"}
	r.cfg.Jev.Disabled = true
	d := r.DecideBrief(context.Background(), in)
	if d == nil || d.need > 0.6 {
		t.Fatalf("need should be capped at the orchestrator's 0.6, got %+v", d)
	}
}

func TestEscalateBriefMovesStrictlyUp(t *testing.T) {
	r := briefRouter(t)
	d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "quick", Brief: "x", ContextTokens: 4000})
	seen := map[string]bool{d.Spec(): true}
	prevCap := r.capabilityOf(d.chosen)
	for next := r.EscalateBrief(d, BriefInput{ContextTokens: 4000}); next != nil; next = r.EscalateBrief(next, BriefInput{ContextTokens: 4000}) {
		if c := r.capabilityOf(next.chosen); c <= prevCap || seen[next.Spec()] {
			t.Fatalf("escalation from cap %.2f went to %s (%.2f)", prevCap, next.Spec(), c)
		}
		prevCap = r.capabilityOf(next.chosen)
		seen[next.Spec()] = true
		d = next
	}
	if prevCap != 1.0 {
		t.Fatalf("escalation should reach the strongest model, stopped at %.2f", prevCap)
	}
}

func TestBriefRoutingOffReturnsNil(t *testing.T) {
	r := briefRouter(t)
	r.SetMode(ModeOff)
	if d := r.DecideBrief(context.Background(), BriefInput{Brief: "x"}); d != nil {
		t.Fatalf("off mode must leave subagents on the orchestrator, got %+v", d)
	}
}

func specs(cands []candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.key()
	}
	return out
}

func TestAffinityBreaksTiesWithinACostClass(t *testing.T) {
	r := briefRouter(t)
	in := BriefInput{ContextTokens: 60000} // too large for both free models
	frontend := BriefClass{Difficulty: map[string]float64{"complex": 1}, Domain: "frontend_ui", DomainP: 0.9}
	backend := BriefClass{Difficulty: map[string]float64{"complex": 1}, Domain: "backend_api", DomainP: 0.9}
	// need 0.7: Luna (0.7) and Sonnet (0.8) both suffice; without affinity
	// the least capable adequate model saves quota.
	if chain := r.rankBrief(in, BriefClass{Difficulty: frontend.Difficulty}, 0.7, ""); chain[0].ref.Model != "gpt-6-luna" {
		t.Fatalf("no domain: got %v", specs(chain))
	}
	if chain := r.rankBrief(in, frontend, 0.7, ""); chain[0].ref.Model != "claude-sonnet-5" {
		t.Fatalf("front-end should prefer Claude, got %v", specs(chain))
	}
	if chain := r.rankBrief(in, backend, 0.7, ""); chain[0].ref.Model != "gpt-6-luna" {
		t.Fatalf("backend should prefer Codex, got %v", specs(chain))
	}
	// Affinity never makes a model adequate: at need 0.9 Sonnet (0.8) is
	// out even for front-end work.
	if chain := r.rankBrief(in, frontend, 0.9, ""); chain[0].ref.Model == "claude-sonnet-5" {
		t.Fatalf("affinity made an inadequate model win: %v", specs(chain))
	}
	// Affinity never moves work to a more expensive class.
	if chain := r.rankBrief(BriefInput{ContextTokens: 4000}, frontend, 0.6, ""); chain[0].ref.Provider != "local" {
		t.Fatalf("free adequate model should win over Claude affinity, got %v", specs(chain))
	}
	zero := 0.0
	r.cfg.AffinityWeight = &zero
	if chain := r.rankBrief(in, frontend, 0.7, ""); chain[0].ref.Model != "gpt-6-luna" {
		t.Fatalf("affinityWeight 0 should disable affinity, got %v", specs(chain))
	}
}

func TestBriefLevelHintSkipsClassification(t *testing.T) {
	r := briefRouter(t)
	d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "medium", Brief: "audit", ContextTokens: 8000, Level: "complex"})
	if d == nil || d.Brief.Source != "rule" || d.need != 0.8 {
		t.Fatalf("a level hint should route by rule at need 0.8, got %+v", d)
	}
}

func TestBriefAvoidFamilyPrefersAnotherEquallyCheapModel(t *testing.T) {
	r := briefRouter(t)
	in := BriefInput{Type: "explore", Effort: "medium", Brief: "audit", ContextTokens: 8000, Level: "complex"}
	base := r.DecideBrief(context.Background(), in)
	in.AvoidFamily = ModelFamily(base.Model)
	d := r.DecideBrief(context.Background(), in)
	if d == nil || ModelFamily(d.Model) == in.AvoidFamily {
		t.Fatalf("avoiding %s still chose %s", in.AvoidFamily, d.Spec())
	}
	if r.cfg.Tiers[d.chosen.tier].Cost != r.cfg.Tiers[base.chosen.tier].Cost {
		t.Fatalf("avoiding a family must not change the cost class: %s → %s", base.Spec(), d.Spec())
	}
}
