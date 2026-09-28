package router

import (
	"fmt"
	"time"
)

// Compact to fit: a model's window may be smaller than the conversation,
// by its nature or because the user set a tighter limit (a local model
// whose prefill is slow). When the model the mode prefers has been
// outgrown, a fresh decision weighs keeping the conversation whole on a
// model that still fits (warm cache, full detail) against compacting it to
// fit the preferred one (a summary call, a cold prefill of the smaller
// prompt, and lost detail). It compacts only when the preferred model is
// clearly better for the mode, and never exceeds a model's window.

const (
	// fitCompactCooldown keeps the router from compacting for the same
	// model again soon after, when the conversation regrew at once.
	fitCompactCooldown = 10 * time.Minute
	// fitQualityMargin is how much stronger quality mode needs the
	// preferred model to be.
	fitQualityMargin = 0.1
	// fitSpeedFactor is how much faster, in expected time to first token,
	// speed and auto mode need the preferred model to be.
	fitSpeedFactor = 2.0
)

// compactAlternative returns a decision that compacts the conversation to
// fit the mode's preferred model, or nil when staying whole is better or
// compacting isn't possible. whole is the decision among the models the
// conversation fits as it is, or nil when none does.
func (r *Router) compactAlternative(class Classification, in Input, whole *Decision) *Decision {
	if in.CompactedTokens == nil {
		return nil
	}
	// The preferred model as if the conversation fit every window.
	pref := r.choose(class, 0)
	if pref == nil {
		return nil
	}
	c := pref.chosen
	tier := r.cfg.Tiers[c.tier]
	if r.fits(tier, c.info, in.ContextTokens) {
		return nil
	}
	after, ok := in.CompactedTokens(c.ref.Provider, c.ref.Model)
	if !ok || after >= in.ContextTokens || !r.fits(tier, c.info, after) || r.fitCompactedRecently(c.key()) {
		return nil
	}
	if whole != nil && !r.clearlyBetter(c, after, whole.chosen, in.ContextTokens, pref.Objective) {
		return nil
	}
	d := *pref
	d.CompactToFit, d.whole = true, whole
	d.ContextTokens, d.At = after, r.now()
	d.Reason = fmt.Sprintf("compacted to fit %s (%s window) · %s → %s tokens · %s · need %.2f · cap %.2f",
		c.ref.Spec(), humanTokens(c.info.ContextWindow), humanTokens(in.ContextTokens), humanTokens(after), class.Kind, pref.need, r.capabilityOf(c))
	if whole == nil {
		d.Reason += " · nothing fits whole"
	}
	d.Reason += objectiveNote(pref.Objective)
	r.mu.Lock()
	r.last = &d
	r.mu.Unlock()
	return &d
}

// clearlyBetter reports whether pref, after compacting to prefTokens, is
// clearly better for the mode than staying whole on whole at wholeTokens:
// quality wants a stronger model, cost a cheaper cost class, speed a much
// faster first token, and auto either.
func (r *Router) clearlyBetter(pref candidate, prefTokens int, whole candidate, wholeTokens int, o Objective) bool {
	cheaper := costRank(r.cfg.Tiers[pref.tier].Cost) < costRank(r.cfg.Tiers[whole.tier].Cost)
	faster := func() bool {
		p, pok := r.expectedTTFT(pref, prefTokens)
		w, wok := r.expectedTTFT(whole, wholeTokens)
		return pok && wok && p*fitSpeedFactor <= w
	}
	switch o {
	case ObjectiveQuality:
		return r.capabilityOf(pref) >= r.capabilityOf(whole)+fitQualityMargin
	case ObjectiveCost:
		return cheaper
	case ObjectiveSpeed:
		return faster()
	}
	return cheaper || faster()
}

func (r *Router) fitCompactedRecently(spec string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.fitCompacted[spec]
	return ok && r.now().Sub(at) < fitCompactCooldown
}

// FitCompacted records that the caller compacted the conversation for d:
// d becomes the sticky orchestrator with a cache to warm.
func (r *Router) FitCompacted(d *Decision) {
	out := *d
	out.CompactToFit, out.whole = false, nil
	r.mu.Lock()
	r.fitCompacted[d.Spec()] = r.now()
	r.orch, r.last, r.cold, r.active = &out, &out, false, r.now()
	r.mu.Unlock()
}

// Whole returns the decision to use when compacting for d failed: the
// model the conversation fits as it is, made sticky; nil when none does.
func (r *Router) Whole(d *Decision) *Decision {
	if d == nil || d.whole == nil {
		return nil
	}
	w := d.whole
	r.mu.Lock()
	r.orch, r.last = w, w
	r.mu.Unlock()
	return w
}
