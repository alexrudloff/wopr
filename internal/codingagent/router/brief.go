package router

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/alexrudloff/wopr/ai"
)

// Subagent brief routing. A brief starts a child with an empty context, so
// switching models costs nothing here: this is where routing pays. Simple
// rules decide first; Jev's typed questions decide the rest; wopr computes
// the context size and the cold-start cost itself.

// Difficulty levels, cheapest first, and the capability each needs.
var difficultyLevels = []struct {
	Name string
	Need float64
}{
	{"mechanical", 0.4},
	{"routine", 0.6},
	{"complex", 0.8},
	{"deep", 0.95},
}

// Asymmetric bars: a cheaper level needs high certainty that it suffices;
// a more capable level needs only moderate evidence that it is needed.
// Published Jev routing misses were all one tier too cheap. Cost mode swaps
// them (see bars).
const (
	cheapBar = 0.7
	upBar    = 0.3
)

// BriefClass is what routing knows about a brief.
type BriefClass struct {
	// Difficulty holds a probability per level name.
	Difficulty map[string]float64 `json:"difficulty"`
	Domain     string             `json:"domain,omitempty"`
	DomainP    float64            `json:"domainP,omitempty"`
	Risk       float64            `json:"risk,omitempty"`
	Judgment   float64            `json:"judgment,omitempty"`
	// Source is "rule" or "jev".
	Source string  `json:"source"`
	Rule   string  `json:"rule,omitempty"`
	Cost   float64 `json:"cost,omitempty"`
	// Secret reports a local secret-scanner hit: the brief stays on
	// trusted (owned) hardware and never reaches Jev.
	Secret bool `json:"secret,omitempty"`
	// Level is the difficulty level the brief was routed at.
	Level string `json:"level,omitempty"`
}

// BriefInput describes one subagent brief.
type BriefInput struct {
	Type   string
	Effort string
	Brief  string
	// ContextTokens is the child's expected peak context; it starts cold,
	// so all of it must be prefilled.
	ContextTokens int
	// Orchestrator is the orchestrator's "provider/model"; a child is not
	// routed above it unless the brief is risky or needs judgment.
	Orchestrator string
	// Saturated reports providers with no free subagent slot.
	Saturated func(provider string) bool
	// Level, when set, is the brief's difficulty level ("routine", ...),
	// which replaces classification: no Jev call.
	Level string
	// AvoidFamily, when set, is a model family ("claude", "gpt", ...) the
	// child should not share, as for an independent check of the
	// orchestrator's work. It only reorders equally cheap adequate models.
	AvoidFamily string
}

// DecideBrief routes a subagent brief, by Jev or, when Jev is not the
// engine or can't answer, by the Basic rules. It returns nil when subagent
// routing is off or nothing is eligible.
func (r *Router) DecideBrief(ctx context.Context, in BriefInput) *Decision {
	if !r.SubagentRouting() {
		return nil
	}
	class, ok := r.classifyBrief(ctx, in)
	if !ok || r.jev == nil {
		// No Jev, or Jev can't answer: the Basic rules route the brief.
		return r.basicBrief(in, class.Secret)
	}
	objective := r.SubagentObjective()
	need, why := briefNeed(class, objective)
	class.Level = difficultyLevels[briefLevel(class, objective)].Name
	if class.Risk >= 0.5 || class.Judgment >= 0.5 {
		class.Level = ""
	}
	if class.Risk < 0.5 && class.Judgment < 0.5 {
		if ceiling, ok := r.specCapability(in.Orchestrator); ok && need > ceiling {
			need, why = ceiling, why+", capped at the orchestrator"
		}
	}
	chain := r.rankBrief(in, class, need, "")
	if len(chain) == 0 {
		return nil
	}
	chain, quota := r.balanceQuota(chain, need)
	if quota != "" {
		why = quota + " · " + why
	}
	if in.AvoidFamily != "" && ModelFamily(chain[0].ref.Model) == in.AvoidFamily {
		first := r.cfg.Tiers[chain[0].tier].Cost
		if i := slices.IndexFunc(chain, func(c candidate) bool {
			return ModelFamily(c.ref.Model) != in.AvoidFamily && r.isAdequate(c, need) &&
				costRank(r.cfg.Tiers[c.tier].Cost) == costRank(first)
		}); i > 0 {
			chain = append([]candidate{chain[i]}, slices.Delete(slices.Clone(chain), i, i+1)...)
			why += ", other family than " + in.AvoidFamily
		}
	}
	return r.briefDecision(in, class, need, why, chain)
}

// ModelFamily is the leading letters of a model id's last path segment,
// lowercased: "claude-sonnet-4" and "anthropic/claude-opus" are "claude",
// "gpt-5-codex" is "gpt", "qwen3-coder" is "qwen".
func ModelFamily(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	end := strings.IndexFunc(model, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(model)
	}
	return strings.ToLower(model[:end])
}

// EscalateBrief returns a route on a more capable model than prev for the
// same brief, or nil when none is usable.
func (r *Router) EscalateBrief(prev *Decision, in BriefInput) *Decision {
	if prev == nil || prev.Brief == nil {
		return nil
	}
	if prev.Basic {
		return r.escalateBasic(prev, in)
	}
	floor := r.capabilityOf(prev.chosen)
	need := floor + 0.01
	chain := slices.DeleteFunc(r.rankBrief(in, *prev.Brief, need, prev.Spec()), func(c candidate) bool {
		return r.capabilityOf(c) <= floor
	})
	if len(chain) == 0 {
		return nil
	}
	d := r.briefDecision(in, *prev.Brief, need, "escalated from "+prev.Spec(), chain)
	d.Fallback = true
	return d
}

func (r *Router) briefDecision(in BriefInput, class BriefClass, need float64, why string, chain []candidate) *Decision {
	chosen := chain[0]
	tier := r.cfg.Tiers[chosen.tier]
	objective := r.SubagentObjective()
	reason := fmt.Sprintf("%s · need %.2f · cap %.2f", why, need, r.capabilityOf(chosen))
	if class.Domain != "" && class.Domain != "general" {
		reason += " · " + class.Domain
	}
	if eta, ok := r.speed.estimate(chosen.key(), in.ContextTokens); ok {
		reason += fmt.Sprintf(" · cold ~%.1fs", eta)
	}
	reason += fmt.Sprintf(" · %s tokens · %s", humanTokens(in.ContextTokens), class.Source) + objectiveNote(objective)
	d := &Decision{
		Tier:          tier.Name,
		Provider:      chosen.ref.Provider,
		Model:         chosen.ref.Model,
		Display:       chosen.info.DisplayName,
		Thinking:      r.briefThinking(need, in.Effort, tier, chosen, objective),
		Class:         Classification{Kind: KindResearch, Source: class.Source},
		Demand:        need,
		need:          need,
		Reason:        reason,
		ContextTokens: in.ContextTokens,
		At:            r.now(),
		Brief:         &class,
		Objective:     objective,
		chosen:        chosen,
		chain:         chain[1:],
	}
	r.mu.Lock()
	r.stats.Briefs++
	r.mu.Unlock()
	return d
}

// classifyBrief applies the fixed rules, then asks Jev. A brief with a
// secret never reaches Jev. When Jev can't answer, ok is false and the
// Basic rules route the brief: there is no guess.
func (r *Router) classifyBrief(ctx context.Context, in BriefInput) (class BriefClass, ok bool) {
	redacted, secret := ScanSecrets(in.Brief)
	switch {
	case secret:
		return BriefClass{Source: "rule", Rule: "secret: owned hardware only", Secret: true, Difficulty: map[string]float64{"routine": 1}}, true
	case in.Level != "":
		return BriefClass{Source: "rule", Rule: "level " + in.Level, Difficulty: map[string]float64{in.Level: 1}}, true
	case in.Effort == "quick":
		return BriefClass{Source: "rule", Rule: "quick lookup", Difficulty: map[string]float64{"mechanical": 1}}, true
	case r.jev == nil:
		return BriefClass{}, false
	}
	class, err := r.jev.classifyBrief(ctx, redacted, in)
	r.mu.Lock()
	r.stats.JevCalls++
	if err != nil {
		r.stats.JevFailures++
	} else {
		r.stats.JevCost += class.Cost
	}
	r.mu.Unlock()
	r.jevAnswered(err)
	return class, err == nil
}

// briefNeed turns difficulty probabilities into a capability need: the
// cheapest level that is likely enough, raised to the most capable level
// with enough support under the objective's bars, then floored by risk and
// judgment.
func briefNeed(class BriefClass, o Objective) (float64, string) {
	level := briefLevel(class, o)
	need := difficultyLevels[level].Need
	why := difficultyLevels[level].Name
	if class.Rule != "" {
		why = class.Rule + " → " + why
	}
	if class.Risk >= 0.5 && need < 0.85 {
		need, why = 0.85, why+", risky"
	}
	if class.Judgment >= 0.5 && need < 0.7 {
		need, why = 0.7, why+", needs judgment"
	}
	return need, why
}

// briefLevel is the index of the brief's difficulty level under the
// objective's asymmetric bars.
func briefLevel(class BriefClass, o Objective) int {
	cheap, up := bars(o)
	level := len(difficultyLevels) - 1
	sum := 0.0
	for i, l := range difficultyLevels {
		sum += class.Difficulty[l.Name]
		if sum >= cheap {
			level = i
			break
		}
	}
	tail := 0.0
	for i := len(difficultyLevels) - 1; i > level; i-- {
		tail += class.Difficulty[difficultyLevels[i].Name]
		if tail >= up {
			level = i
			break
		}
	}
	return level
}

// specCapability is the configured capability of provider/model.
func (r *Router) specCapability(spec string) (float64, bool) {
	for i, ref := range r.cfg.refs(spec) {
		return r.capabilityOf(candidate{tier: i, ref: ref}), true
	}
	return 0, false
}

// rankBrief orders the usable models for a brief. Models whose cold start
// for the expected context would exceed MaxColdStartSeconds are dropped
// unless nothing else fits; secret briefs keep to owned hardware. Adequate
// models come first, by cost class, then speed (a model within 1.5x of the
// class's fastest cold start counts as equally fast), then domain affinity,
// then the least capability that suffices, which saves scarce quota.
//
// Speed mode orders adequate models by expected cold start alone, with
// unmeasured models counted as slow and pay-per-token last. Quality mode
// keeps only the band near the strongest reachable model, ignores need,
// and prefers affinity, then speed, then strength. Cost mode orders adequate
// models by cost class, then the quota their plan has left, then affinity,
// then the least capability that suffices, then cold start.
func (r *Router) rankBrief(in BriefInput, class BriefClass, need float64, exclude string) []candidate {
	type scored struct {
		c         candidate
		eta       float64
		measured  bool
		saturated bool
	}
	maxCold := r.cfg.Subagents.MaxColdStartSeconds
	objective := r.SubagentObjective()
	var cands []candidate
	for _, c := range r.allCandidates(in.ContextTokens, objective) {
		cost := r.cfg.Tiers[c.tier].Cost
		if c.key() == exclude || (class.Secret && cost != CostFreeLocal && cost != CostFreeRemote) {
			continue
		}
		cands = append(cands, c)
	}
	if objective == ObjectiveQuality {
		cands, need = r.band(cands, in.Saturated), 0
	}
	etas := r.etas(cands, in.ContextTokens, true)
	var pool, slow []scored
	for _, c := range cands {
		eta, measured := r.speed.estimate(c.key(), in.ContextTokens)
		s := scored{c: c, eta: eta, measured: measured, saturated: in.Saturated != nil && in.Saturated(c.ref.Provider)}
		if measured && maxCold > 0 && eta > maxCold {
			slow = append(slow, s)
			continue
		}
		pool = append(pool, s)
	}
	unpaid := slices.ContainsFunc(pool, func(s scored) bool {
		return r.cfg.Tiers[s.c.tier].Cost != CostPaid && r.isAdequate(s.c, need)
	})
	if unpaid || !r.cfg.PaidLastResort {
		pool = slices.DeleteFunc(pool, func(s scored) bool { return r.cfg.Tiers[s.c.tier].Cost == CostPaid })
	}
	var quota map[string]int
	if objective == ObjectiveCost {
		quota = r.quotaBands(cands)
	}
	fastest := map[int]float64{}
	for _, s := range pool {
		cr := costRank(r.cfg.Tiers[s.c.tier].Cost)
		if f, ok := fastest[cr]; s.measured && (!ok || s.eta < f) {
			fastest[cr] = s.eta
		}
	}
	bucket := func(s scored) int {
		f, ok := fastest[costRank(r.cfg.Tiers[s.c.tier].Cost)]
		if !s.measured || !ok || s.eta <= max(1.5*f, f+2) {
			return 0
		}
		return 1
	}
	group := func(s scored) int {
		switch {
		case r.isAdequate(s.c, need) && !s.saturated:
			return 0
		case r.isAdequate(s.c, need):
			return 1
		}
		return 2
	}
	slices.SortStableFunc(pool, func(a, b scored) int {
		if ga, gb := group(a), group(b); ga != gb {
			return ga - gb
		}
		if group(a) == 2 {
			return cmpFloatDesc(r.capabilityOf(a.c), r.capabilityOf(b.c))
		}
		switch objective {
		case ObjectiveSpeed:
			return cmp.Or(
				cmp.Compare(r.paid(a.c), r.paid(b.c)),
				cmp.Compare(etas[a.c.key()], etas[b.c.key()]),
				cmpFloatDesc(r.affinity(a.c, class), r.affinity(b.c, class)),
				cmp.Compare(r.capabilityOf(a.c), r.capabilityOf(b.c)),
			)
		case ObjectiveQuality:
			return cmp.Or(
				cmpFloatDesc(r.affinity(a.c, class), r.affinity(b.c, class)),
				cmp.Compare(etas[a.c.key()], etas[b.c.key()]),
				cmpFloatDesc(r.capabilityOf(a.c), r.capabilityOf(b.c)),
			)
		case ObjectiveCost:
			return cmp.Or(
				costRank(r.cfg.Tiers[a.c.tier].Cost)-costRank(r.cfg.Tiers[b.c.tier].Cost),
				cmp.Compare(quota[b.c.key()], quota[a.c.key()]),
				cmpFloatDesc(r.affinity(a.c, class), r.affinity(b.c, class)),
				cmp.Compare(r.capabilityOf(a.c), r.capabilityOf(b.c)),
				cmp.Compare(a.eta, b.eta),
			)
		}
		return cmp.Or(
			costRank(r.cfg.Tiers[a.c.tier].Cost)-costRank(r.cfg.Tiers[b.c.tier].Cost),
			bucket(a)-bucket(b),
			cmpFloatDesc(r.affinity(a.c, class), r.affinity(b.c, class)),
			cmp.Compare(r.capabilityOf(a.c), r.capabilityOf(b.c)),
			cmp.Compare(a.eta, b.eta),
		)
	})
	slices.SortStableFunc(slow, func(a, b scored) int { return cmp.Compare(a.eta, b.eta) })
	out := make([]candidate, 0, len(pool)+len(slow))
	for _, s := range append(pool, slow...) {
		out = append(out, s.c)
	}
	return out
}

// affinity is the model's domain bonus weighted by how sure Jev is of the
// domain. It only breaks ties among adequate models at the same cost.
func (r *Router) affinity(c candidate, class BriefClass) float64 {
	if class.Domain == "" {
		return 0
	}
	return c.ref.Affinity[class.Domain] * class.DomainP * r.cfg.affinityWeight()
}

// briefThinking picks a child's thinking level from its need, shifted by
// the objective, and capped by the effort, the model, and the tier.
func (r *Router) briefThinking(need float64, effort string, tier TierConfig, c candidate, o Objective) (level ai.ThinkingLevel) {
	if c.info.MaxThinking == "" {
		return ai.ThinkingOff
	}
	ladder := []ai.ThinkingLevel{ai.ThinkingOff, ai.ThinkingLow, ai.ThinkingMedium, ai.ThinkingHigh}
	step := 0
	switch {
	case need >= 0.9:
		step = 2
	case need >= 0.7:
		step = 1
	}
	level = ladder[min(max(step+thinkingShift(o), 0), len(ladder)-1)]
	if effort == "quick" && rank(level) > rank(ai.ThinkingLow) {
		level = ai.ThinkingLow
	}
	for _, cap := range []string{c.ref.Thinking, tier.MaxThinking, string(c.info.MaxThinking)} {
		if cap != "" && rank(ai.ThinkingLevel(cap)) < rank(level) {
			level = ai.ThinkingLevel(cap)
		}
	}
	return level
}

// BriefSummary is a one-line description of a brief decision.
func (d *Decision) BriefSummary() string {
	if d == nil || d.Brief == nil {
		return ""
	}
	parts := []string{d.Spec(), d.Tier}
	if d.Brief.Domain != "" {
		parts = append(parts, d.Brief.Domain)
	}
	return strings.Join(parts, " · ")
}
