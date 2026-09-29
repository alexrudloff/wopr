package router

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/alexrudloff/wopr/ai"
)

// Objective is what routing optimizes for the models the user has not
// pinned. The UI calls it the routing mode.
type Objective string

// Routing objectives.
const (
	// ObjectiveAuto: the cheapest model likely to suffice, via Jev.
	ObjectiveAuto Objective = "auto"
	// ObjectiveCost: the fewest marginal dollars, then the least
	// subscription quota, then time. Free tiers serve unless one is clearly
	// not enough, the subscription is the ceiling, pay-per-token is never
	// used, and thinking is one step lower.
	ObjectiveCost Objective = "cost"
	// ObjectiveSpeed: the least expected time to an answer among models
	// capable enough, with one step less thinking.
	ObjectiveSpeed Objective = "speed"
	// ObjectiveQuality: a model within qualityBand of the strongest one
	// reachable, cost ignored short of pay-per-token, with one step more
	// thinking.
	ObjectiveQuality Objective = "quality"
	// ObjectiveUncensored: only models flagged Uncensored, orchestrator and
	// subagents alike, and no Jev (it relays to a third party).
	ObjectiveUncensored Objective = "uncensored"
	// ObjectivePrivate: only models on private connections, orchestrator,
	// subagents, and side tasks alike, highest-ranked first; Jev only when
	// it is marked Privacy Safe too. Nothing falls back to another model.
	ObjectivePrivate Objective = "private"
)

// qualityBand is how far below the strongest reachable model a model may be
// and still serve in quality mode.
const qualityBand = 0.05

// Unmeasured models are assumed this slow in speed and quality mode, and
// always a second slower than the slowest measured candidate, so they are
// never preferred blindly.
const (
	unmeasuredBaseSeconds = 4.0
	unmeasuredPerKSeconds = 0.5
)

// ParseObjective parses a mode name.
func ParseObjective(name string) (Objective, bool) {
	switch o := Objective(strings.ToLower(strings.TrimSpace(name))); o {
	case ObjectiveAuto, ObjectiveCost, ObjectiveSpeed, ObjectiveQuality, ObjectiveUncensored, ObjectivePrivate:
		return o, true
	}
	return "", false
}

// Objective returns the routing objective.
func (r *Router) Objective() Objective {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.objective
}

// Objectives lists the objectives on offer, in the order Tab cycles them:
// none without routing, and uncensored and private only when a configured
// model is flagged for them.
func (r *Router) Objectives() []Objective {
	if !r.Available() {
		return nil
	}
	out := []Objective{ObjectiveAuto, ObjectiveCost, ObjectiveSpeed, ObjectiveQuality}
	if r.UncensoredAvailable() {
		out = append(out, ObjectiveUncensored)
	}
	if r.PrivateAvailable() {
		out = append(out, ObjectivePrivate)
	}
	return out
}

// PrivateAvailable reports whether any configured model is on a private
// connection.
func (r *Router) PrivateAvailable() bool { return r.cfg.hasPrivacySafe() }

// IsPrivacySafe reports whether provider/model is on a private connection.
func (r *Router) IsPrivacySafe(spec string) bool {
	provider, _, _ := strings.Cut(spec, "/")
	return r.cfg.private(provider)
}

// owned reports whether a model keeps data with the user, for sensitive
// prompts and secret briefs: on a private connection, or, when the user
// marked none, in a free-local or free-remote tier.
func (r *Router) owned(tier TierConfig, ref ModelRef) bool {
	if r.cfg.hasPrivacySafe() {
		return r.cfg.private(ref.Provider)
	}
	return tier.Cost == CostFreeLocal || tier.Cost == CostFreeRemote
}

// consultsJev reports whether Jev classifies prompts under o: private mode
// sends nothing to a Jev endpoint not marked Privacy Safe.
func (r *Router) consultsJev(o Objective) bool {
	return r.jev != nil && (o != ObjectivePrivate || r.cfg.Jev.PrivacySafe)
}

// UncensoredAvailable reports whether any configured model is flagged
// Uncensored.
func (r *Router) UncensoredAvailable() bool { return r.cfg.hasUncensored() }

// IsUncensored reports whether provider/model is flagged Uncensored.
func (r *Router) IsUncensored(spec string) bool {
	for _, ref := range r.cfg.refs(spec) {
		if ref.Uncensored {
			return true
		}
	}
	return false
}

// Hashline reports the model's own hashline flag (nil when unset) and
// whether a free-local or free-remote tier lists provider/model.
func (r *Router) Hashline(spec string) (flag *bool, free bool) {
	for i, ref := range r.cfg.refs(spec) {
		cost := r.cfg.Tiers[i].Cost
		return ref.Hashline, cost == CostFreeLocal || cost == CostFreeRemote
	}
	return nil, false
}

// Effort returns the configured default thinking level for provider/model,
// or "" when none is set.
func (r *Router) Effort(spec string) string {
	for _, ref := range r.cfg.refs(spec) {
		if ref.Effort != "" {
			return ref.Effort
		}
	}
	return ""
}

// EscalateThinking reports whether failed turns raise the thinking level.
func (r *Router) EscalateThinking() bool {
	return r.cfg.EscalateThinking != nil && *r.cfg.EscalateThinking
}

// checkObjective reports whether o can be chosen now.
func (r *Router) checkObjective(o Objective) error {
	if _, ok := ParseObjective(string(o)); !ok {
		return fmt.Errorf("router: unknown mode %q", o)
	}
	if !r.Available() {
		return errors.New("model routing is off: turn it on in /setup → Model routing")
	}
	if o == ObjectiveUncensored && !r.UncensoredAvailable() {
		return fmt.Errorf("router: no configured model is flagged uncensored")
	}
	if o == ObjectivePrivate && !r.PrivateAvailable() {
		return fmt.Errorf("router: no configured model is on a private connection")
	}
	return nil
}

// SetObjective lets the router pick the orchestrator under o. The
// subagents' choice is their own and does not change. The sticky
// orchestrator is forgotten so the next turn decides afresh.
func (r *Router) SetObjective(o Objective) error {
	if err := r.checkObjective(o); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.objective, r.auto = o, true
	r.orch, r.cold = nil, true
	return nil
}

// SubagentObjective is the mode subagent briefs and side tasks are routed
// under while subagent routing is on.
func (r *Router) SubagentObjective() Objective {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subObjective
}

// SetSubagentObjective routes subagent briefs and side tasks under o,
// whatever picks the orchestrator.
func (r *Router) SetSubagentObjective(o Objective) error {
	if err := r.checkObjective(o); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subObjective, r.subagents = o, true
	return nil
}

// StopSubagentRouting runs subagents and side tasks on a model the caller
// picks (a chosen model, or the orchestrator's) instead of routing them.
func (r *Router) StopSubagentRouting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subagents, r.subObjective = false, ObjectiveAuto
}

// allowed reports whether objective o lets a model of the tier serve:
// uncensored takes flagged models only, and cost never pays per token.
func (r *Router) allowed(tier TierConfig, ref ModelRef, o Objective) bool {
	switch o {
	case ObjectiveUncensored:
		return ref.Uncensored
	case ObjectivePrivate:
		return r.cfg.private(ref.Provider)
	case ObjectiveCost:
		return tier.Cost != CostPaid
	}
	return true
}

// Admits reports whether objective o lets provider/model serve at all, as
// when it is the user's own model and routing found nothing. Uncensored
// admits flagged models; cost admits only models listed in a free or
// subscription tier, since an unlisted model may bill per token.
func (r *Router) Admits(o Objective, spec string) bool {
	switch o {
	case ObjectiveUncensored:
		return r.IsUncensored(spec)
	case ObjectivePrivate:
		return r.IsPrivacySafe(spec)
	case ObjectiveCost:
		for i := range r.cfg.refs(spec) {
			if r.cfg.Tiers[i].Cost != CostPaid {
				return true
			}
		}
		return false
	}
	return true
}

// costTolerance is how far an orchestrator's need may exceed a model's
// capability, beyond Slack, before cost mode moves up a cost class. Jev
// answers the orchestrator's questions with scores rather than level
// probabilities, so this is the orchestrator's form of cost's flipped bars:
// only a clear shortfall spends quota.
const costTolerance = 0.15

// bars returns the brief bars for the objective: the cheapest difficulty
// level with P(level or easier) >= cheap, raised to the hardest level with
// P(level or harder) >= up. Auto goes up when unsure; cost flips the bars,
// so stepping up needs high certainty that the cheaper level will not do.
func bars(o Objective) (cheap, up float64) {
	if o == ObjectiveCost {
		return upBar, cheapBar
	}
	return cheapBar, upBar
}

// subscriptionPlans maps a subscription provider to the plan whose usage
// windows its responses report.
var subscriptionPlans = map[string]string{
	"openai-codex": ai.SubscriptionChatGPT,
	"anthropic":    ai.SubscriptionClaude,
}

// unknownQuota is the share of allowance assumed left when a plan has not
// reported usage: the middle, so a plan known to have plenty left wins and
// one near its limit loses.
const unknownQuota = 50.0

// quotaBands scores each candidate's provider by the subscription allowance
// left in its tightest unexpired usage window, in 10-point bands so small
// differences fall through to capability fit. Higher means more left;
// models outside a subscription tier score as unknown.
func (r *Router) quotaBands(cands []candidate) map[string]int {
	plans := r.usage()
	now := r.now()
	out := map[string]int{}
	for _, c := range cands {
		left := unknownQuota
		if plan, ok := subscriptionPlans[c.ref.Provider]; ok && r.cfg.Tiers[c.tier].Cost == CostSubscription {
			for _, p := range plans {
				if p.Plan != plan {
					continue
				}
				left = 100
				for _, w := range p.Windows {
					if w.ResetsAt.IsZero() || w.ResetsAt.After(now) {
						left = min(left, 100-w.UsedPercent)
					}
				}
			}
		}
		out[c.key()] = int(math.Floor(left / 10))
	}
	return out
}

// etas returns each candidate's expected seconds to first token. With
// conservative set, an unmeasured model counts as slow: a second slower
// than both the slowest measured candidate and the unmeasured default.
// Otherwise it counts as instant, so auto mode gets it measured.
func (r *Router) etas(cands []candidate, contextTokens int, conservative bool) map[string]float64 {
	out := make(map[string]float64, len(cands))
	slowest := unmeasuredBaseSeconds + unmeasuredPerKSeconds*float64(contextTokens)/1000
	var unmeasured []string
	for _, c := range cands {
		t, ok := r.expectedTTFT(c, contextTokens)
		if !ok {
			unmeasured = append(unmeasured, c.key())
			continue
		}
		out[c.key()] = t
		slowest = max(slowest, t)
	}
	for _, key := range unmeasured {
		if conservative {
			out[key] = slowest + 1
		} else {
			out[key] = 0
		}
	}
	return out
}

// band keeps the candidates within qualityBand of the strongest one.
// Pay-per-token models only count when nothing else is left, and saturated
// providers only when every candidate is saturated.
func (r *Router) band(cands []candidate, saturated func(string) bool) []candidate {
	pool := cands
	if unpaid := filter(pool, func(c candidate) bool { return r.paid(c) == 0 }); len(unpaid) > 0 {
		pool = unpaid
	} else if !r.cfg.PaidLastResort {
		return nil
	}
	if saturated != nil {
		if free := filter(pool, func(c candidate) bool { return !saturated(c.ref.Provider) }); len(free) > 0 {
			pool = free
		}
	}
	best := 0.0
	for _, c := range pool {
		best = max(best, r.capabilityOf(c))
	}
	return filter(pool, func(c candidate) bool { return r.capabilityOf(c) >= best-qualityBand-1e-9 })
}

// paid is 1 for a pay-per-token candidate, else 0.
func (r *Router) paid(c candidate) int {
	if r.cfg.Tiers[c.tier].Cost == CostPaid {
		return 1
	}
	return 0
}

func filter(cands []candidate, keep func(candidate) bool) []candidate {
	var out []candidate
	for _, c := range cands {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// thinkingShift is the objective's thinking adjustment in ladder steps.
func thinkingShift(o Objective) int {
	switch o {
	case ObjectiveSpeed, ObjectiveCost:
		return -1
	case ObjectiveQuality:
		return 1
	}
	return 0
}

// objectiveNote tags a reason with a non-default objective.
func objectiveNote(o Objective) string {
	if o == "" || o == ObjectiveAuto {
		return ""
	}
	return " · " + string(o)
}
