package router

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// The Basic rules route without Jev: when routing is set to Basic, and for
// any turn Jev can't answer. They are fixed and use only what the user set
// up about each model (its place in the ranking, its window, its cost
// class, the uncensored flag) and what wopr measured (speed, tool calling:
// a model that can't call tools is never routed). They never name a model.
//
// A model is eligible when it is reachable, not resting after a failure or
// a quota error, allowed by the mode, and the conversation plus room for the
// reply fits its window. Among eligible models:
//
//	auto, quality, uncensored  the highest-ranked
//	speed                      the least expected time to a reply
//	cost                       local, then free remote, then subscription;
//	                           the highest-ranked within a class
//
// Pay-per-token models come last, and only with PaidLastResort; cost mode
// never uses them. A model stays while it is eligible (its cache is warm);
// a new session, a compaction, or a mode change picks afresh, and a failure
// moves to the next model in the order. Thinking is the model's own effort
// setting, else the session's level: no automatic boosts. Strength tags are
// not used: telling what a prompt is about is Jev's job.

// basicReplyTokens is the reply length speed mode assumes when it adds a
// model's decode time to its time to first token.
const basicReplyTokens = 500

// decideBasic picks the orchestrator by the Basic rules and makes it the
// sticky decision.
func (r *Router) decideBasic(contextTokens int, now time.Time) *Decision {
	r.mu.Lock()
	pinned, objective := r.pinned, r.objective
	r.mu.Unlock()
	pool := r.allCandidates(contextTokens, objective)
	if pinned != "" {
		pool = r.tierCandidates(r.cfg.tierIndex(pinned), contextTokens, objective)
	}
	chain := r.basicOrder(r.paidLast(pool), contextTokens, objective)
	quota := ""
	if pinned == "" {
		chain, quota = r.balanceQuota(chain, 0)
	}
	d := r.basicDecision(chain, contextTokens, objective)
	if d == nil {
		return nil
	}
	if quota != "" {
		d.Reason = quota + " · " + d.Reason
	}
	if pinned != "" {
		d.Reason += " · pinned to " + pinned
	}
	r.mu.Lock()
	r.orch, r.last, r.cold, r.active = d, d, false, now
	r.stats.Decisions++
	r.mu.Unlock()
	return d
}

// paidLast drops pay-per-token candidates unless PaidLastResort allows them
// (basicOrder puts them last).
func (r *Router) paidLast(pool []candidate) []candidate {
	if r.cfg.PaidLastResort {
		return pool
	}
	return filter(pool, func(c candidate) bool { return r.paid(c) == 0 })
}

// basicOrder orders eligible candidates by the Basic rules for mode o:
// unpaid before pay-per-token, then by the mode's rule, then by rank.
func (r *Router) basicOrder(pool []candidate, contextTokens int, o Objective) []candidate {
	var reply map[string]float64
	if o == ObjectiveSpeed {
		reply = r.replySeconds(pool, contextTokens)
	}
	out := slices.Clone(pool)
	slices.SortStableFunc(out, func(a, b candidate) int {
		if c := cmp.Compare(r.paid(a), r.paid(b)); c != 0 {
			return c
		}
		switch o {
		case ObjectiveCost:
			if c := cmp.Compare(basicCostRank(r.cfg.Tiers[a.tier].Cost), basicCostRank(r.cfg.Tiers[b.tier].Cost)); c != 0 {
				return c
			}
		case ObjectiveSpeed:
			if c := cmp.Compare(reply[a.key()], reply[b.key()]); c != 0 {
				return c
			}
		}
		return cmpFloatDesc(r.capabilityOf(a), r.capabilityOf(b))
	})
	return out
}

// basicCostRank orders cost classes for the Basic rules: local, free
// remote, subscription, pay-per-token.
func basicCostRank(cost string) int {
	switch cost {
	case CostFreeLocal:
		return 0
	case CostFreeRemote:
		return 1
	case CostSubscription:
		return 2
	}
	return 3
}

// replySeconds is each candidate's expected time to a reply: its time to
// first token for contextTokens (unmeasured counts as slow) plus
// basicReplyTokens at its measured decode rate. A model with no measured
// rate is charged the slowest rate measured among the candidates.
func (r *Router) replySeconds(cands []candidate, contextTokens int) map[string]float64 {
	out := r.etas(cands, contextTokens, true)
	slowest := 0.0
	for _, c := range cands {
		if rate := r.speed.decodeRate(c.key()); rate > 0 && (slowest == 0 || rate < slowest) {
			slowest = rate
		}
	}
	for _, c := range cands {
		rate := r.speed.decodeRate(c.key())
		if rate <= 0 {
			rate = slowest
		}
		if rate > 0 {
			out[c.key()] += basicReplyTokens / rate
		}
	}
	return out
}

// basicDecision is the decision for the head of a Basic order, the rest
// being its failover chain; nil when chain is empty.
func (r *Router) basicDecision(chain []candidate, contextTokens int, o Objective) *Decision {
	if len(chain) == 0 {
		return nil
	}
	chosen := chain[0]
	tier := r.cfg.Tiers[chosen.tier]
	reason := "basic · " + basicRule(o, tier.Cost)
	if o == ObjectiveSpeed {
		if eta, ok := r.expectedTTFT(chosen, contextTokens); ok {
			reason += fmt.Sprintf(" · ~%.1fs to first token", eta)
		}
	}
	reason += fmt.Sprintf(" · %s tokens", humanTokens(contextTokens)) + objectiveNote(o)
	return &Decision{
		Tier:          tier.Name,
		Provider:      chosen.ref.Provider,
		Model:         chosen.ref.Model,
		Display:       chosen.info.DisplayName,
		Thinking:      ai.ThinkingLevel(chosen.ref.Effort),
		Class:         Classification{Source: EngineBasic},
		Reason:        reason,
		Objective:     o,
		ContextTokens: contextTokens,
		At:            r.now(),
		Basic:         true,
		chosen:        chosen,
		chain:         chain[1:],
	}
}

// basicRule names the rule that picked a model of cost class cost.
func basicRule(o Objective, cost string) string {
	switch {
	case cost == CostPaid:
		return "pay-per-token: nothing else eligible"
	case o == ObjectiveSpeed:
		return "fastest reply"
	case o == ObjectiveCost:
		return "cheapest class (" + cost + "), highest ranked"
	}
	return "highest ranked"
}

// basicBrief routes a subagent brief by the Basic rules under the
// subagents' mode, sized by the context the child is expected to reach. A
// brief carrying a secret keeps to owned hardware.
func (r *Router) basicBrief(in BriefInput, secret bool) *Decision {
	o := r.SubagentObjective()
	pool := r.paidLast(r.allCandidates(in.ContextTokens, o))
	if secret {
		pool = filter(pool, func(c candidate) bool {
			cost := r.cfg.Tiers[c.tier].Cost
			return cost == CostFreeLocal || cost == CostFreeRemote
		})
	}
	chain, quota := r.balanceQuota(r.basicOrder(pool, in.ContextTokens, o), 0)
	d := r.basicDecision(chain, in.ContextTokens, o)
	if d == nil {
		return nil
	}
	if quota != "" {
		d.Reason = quota + " · " + d.Reason
	}
	d.Brief = &BriefClass{Source: EngineBasic, Secret: secret}
	r.mu.Lock()
	r.stats.Briefs++
	r.mu.Unlock()
	return d
}

// escalateBasic moves a Basic brief to the next stronger eligible model.
func (r *Router) escalateBasic(prev *Decision, in BriefInput) *Decision {
	o := r.SubagentObjective()
	floor := r.capabilityOf(prev.chosen)
	pool := filter(r.paidLast(r.allCandidates(in.ContextTokens, o)), func(c candidate) bool {
		if prev.Brief.Secret && r.cfg.Tiers[c.tier].Cost != CostFreeLocal && r.cfg.Tiers[c.tier].Cost != CostFreeRemote {
			return false
		}
		return r.capabilityOf(c) > floor
	})
	// The next step up: unpaid first, then the least capability above.
	slices.SortStableFunc(pool, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(r.paid(a), r.paid(b)), cmp.Compare(r.capabilityOf(a), r.capabilityOf(b)))
	})
	d := r.basicDecision(pool, in.ContextTokens, o)
	if d == nil {
		return nil
	}
	d.Brief, d.Fallback = prev.Brief, true
	d.Reason = "escalated from " + prev.Spec() + " · " + d.Reason
	return d
}

// basicSideOrder orders side-task candidates: the cheapest cost class,
// then the highest-ranked model in it.
func (r *Router) basicSideOrder(pool []candidate) []candidate {
	out := slices.Clone(pool)
	slices.SortStableFunc(out, func(a, b candidate) int {
		if c := cmp.Compare(basicCostRank(r.cfg.Tiers[a.tier].Cost), basicCostRank(r.cfg.Tiers[b.tier].Cost)); c != 0 {
			return c
		}
		return cmpFloatDesc(r.capabilityOf(a), r.capabilityOf(b))
	})
	return out
}

// NoRouteReason explains why no configured model can take a conversation
// of contextTokens under mode o, for the error the caller shows instead of
// falling back to a model the mode didn't choose.
func (r *Router) NoRouteReason(contextTokens int, o Objective) string {
	var unreachable, resting, excluded, signedOut, small, total int
	for idx, tier := range r.cfg.Tiers {
		up := r.probe(tier)
		for _, ref := range tier.Models {
			total++
			info, configured := r.host.ModelInfo(ref.Provider, ref.Model)
			switch {
			case !r.allowed(tier, ref, o) || (tier.Cost == CostPaid && !r.cfg.PaidLastResort):
				excluded++
			case !up:
				unreachable++
			case r.isDown(ref.Spec()):
				resting++
			case !configured:
				signedOut++
			case !r.fits(r.cfg.Tiers[idx], info, contextTokens):
				small++
			}
		}
	}
	if total == 0 {
		return "no models are set up for routing: add some in /setup"
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	switch o {
	case ObjectiveCost:
		add(excluded, "pay per token, which cost mode never uses")
	case ObjectiveUncensored:
		add(excluded, "not marked abliterated")
	default:
		add(excluded, "pay per token and not allowed as a last resort")
	}
	add(unreachable, "unreachable")
	add(resting, "resting after errors or a quota limit")
	add(signedOut, "not signed in")
	add(small, "too small for "+humanTokens(contextTokens)+" tokens")
	return fmt.Sprintf("%s mode: none of your %d routed models can take this request (%s)", o, total, strings.Join(parts, ", "))
}
