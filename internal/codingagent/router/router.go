package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/text"
)

// ModelInfo is what the router needs to know about a candidate model.
type ModelInfo struct {
	DisplayName     string
	ContextWindow   int
	MaxOutputTokens int
	// MaxThinking is the model's top thinking level, or "" when the model
	// has no thinking.
	MaxThinking ai.ThinkingLevel
}

// Host answers the router's questions about the surrounding harness.
type Host interface {
	// ModelInfo reports whether provider/model is configured and authenticated
	// and returns its capabilities.
	ModelInfo(provider, model string) (ModelInfo, bool)
	// APIKey returns the API key for a provider, or "".
	APIKey(provider string) string
}

// Input describes the prompt to route.
type Input struct {
	Prompt string
	// ContextTokens is the estimated size of the conversation the request
	// will carry.
	ContextTokens int
	// CompactedTokens, when set, estimates the conversation's size after
	// compacting it for provider/model, and reports false when it can't be
	// compacted. The caller sets it only at a natural boundary (a new
	// prompt), where the router may compact to fit a model the conversation
	// has outgrown.
	CompactedTokens func(provider, model string) (int, bool)
}

// Decision is the router's choice for one prompt.
type Decision struct {
	Tier     string
	Provider string
	Model    string
	Display  string
	Thinking ai.ThinkingLevel
	Class    Classification
	Demand   float64
	need     float64
	Reason   string
	// Fallback reports that the decision replaced a failed one.
	Fallback      bool
	ContextTokens int
	At            time.Time
	// Brief is set on subagent brief decisions.
	Brief *BriefClass
	// Objective is the routing mode the decision was made under.
	Objective Objective
	// CompactToFit asks the caller to compact the conversation for the
	// decision's model before the request; ContextTokens is the size after.
	CompactToFit bool
	// Basic reports a decision made by the Basic rules. Its Thinking is the
	// model's own effort setting, or "" for the session's level.
	Basic bool

	chosen candidate
	chain  []candidate
	// whole is the decision without compacting, used when the compaction
	// fails; nil when nothing fits whole.
	whole *Decision
}

// Spec returns the "provider/model" form.
func (d *Decision) Spec() string { return d.Provider + "/" + d.Model }

func (d *Decision) candidate() candidate { return d.chosen }

// Summary is a one-line description for the transcript and status bar.
func (d *Decision) Summary() string {
	if d == nil {
		return "no route"
	}
	thinking := cmp.Or(string(d.Thinking), "off")
	prefix := "routed"
	if d.Fallback {
		prefix = "rerouted"
	}
	return fmt.Sprintf("%s to %s (%s) · %s · thinking %s", prefix, d.Spec(), d.Tier, d.Class.Kind, thinking)
}

// Failure describes why the current model must be abandoned.
type Failure struct {
	Message string
	// Overflow means the context did not fit; the next model needs a
	// larger window.
	Overflow bool
	// RateLimited means a quota or rate-limit response, which rests the
	// provider for QuotaBackoffMinutes.
	RateLimited bool
	// ContextTokens is the size the next model must fit.
	ContextTokens int
}

type candidate struct {
	tier int
	ref  ModelRef
	info ModelInfo
}

func (c candidate) key() string { return c.ref.Spec() }

type probeResult struct {
	ok  bool
	at  time.Time
	err string
}

// Stats counts router activity for /router status.
type Stats struct {
	Decisions   int
	JevCalls    int
	JevFailures int
	JevCost     float64
	Fallbacks   int
	Briefs      int
}

// Mode is who picks the models.
type Mode string

// Routing modes.
const (
	// ModeAuto: Jev picks the orchestrator (sticky while its cache is warm)
	// and its thinking level, and routes every subagent brief.
	ModeAuto Mode = "auto"
	// ModePinned: the user's model and thinking orchestrate; subagent briefs
	// and side tasks are still routed.
	ModePinned Mode = "pinned"
	// ModeOff: the user's model does everything, subagents included.
	ModeOff Mode = "off"
)

// Router picks models. It is safe for concurrent use.
type Router struct {
	mu  sync.Mutex
	cfg Config
	jev *jevClient
	// host answers model questions; it is read-only after New.
	host Host
	// auto means Jev picks the orchestrator; subagents means subagent
	// briefs and side tasks are routed. Auto implies subagents.
	auto      bool
	subagents bool
	pinned    string
	// objective is the orchestrator's mode, subObjective the subagents'.
	objective    Objective
	subObjective Objective
	// orch is the sticky orchestrator decision; cold forces the next turn
	// to re-decide it freely; active is the last time the orchestrator's
	// cache was used.
	orch   *Decision
	cold   bool
	active time.Time
	// down maps "provider/model" to the time the model may be tried again.
	down   map[string]time.Time
	probes map[string]probeResult
	last   *Decision
	stats  Stats
	now    func() time.Time
	client *http.Client
	speed  *speedBook
	// lastSpec and lastTokens describe the previous request, whose prefix
	// that model now has cached.
	lastSpec   string
	lastTokens int
	// served holds the models that answered a request in this process.
	served map[string]bool
	// usage reports the subscription plans' latest usage windows.
	usage func() []ai.PlanUsage
	// paused reports that Jev stopped answering; pauseNotice says so once.
	paused      bool
	pauseNotice string
	// fitCompacted maps "provider/model" to when a conversation was last
	// compacted to fit it.
	fitCompacted map[string]time.Time
	// quotaBalance moves work between similarly ranked subscription models
	// as a plan gets tight (see balanceQuota).
	quotaBalance bool
}

// New builds a router from cfg. host must not be nil.
func New(cfg Config, host Host) *Router {
	r := &Router{
		cfg:          cfg,
		host:         host,
		auto:         cfg.Enabled,
		subagents:    cfg.Enabled,
		objective:    cmp.Or(cfg.objective, ObjectiveAuto),
		subObjective: cmp.Or(cfg.objective, ObjectiveAuto),
		down:         map[string]time.Time{},
		probes:       map[string]probeResult{},
		served:       map[string]bool{},
		fitCompacted: map[string]time.Time{},
		quotaBalance: true,
		now:          time.Now,
		usage:        ai.SubscriptionUsage,
		// Probes are rare and must not leave pooled connections behind.
		client: &http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}},
		speed:  loadSpeedBook(cfg.statsDir),
	}
	if cfg.EngineName() == EngineJev && cfg.Jev.Active() {
		r.jev = newJevClient(cfg.Jev, func() string {
			if cfg.Jev.APIKeyProvider == "none" {
				return ""
			}
			return host.APIKey(cfg.Jev.APIKeyProvider)
		})
	}
	return r
}

// Config returns the active configuration.
func (r *Router) Config() Config { return r.cfg }

// Available reports whether model routing is on, by the Basic rules or by
// Jev. Without it the session runs on the model the user selects,
// subagents included, and no mode is offered.
func (r *Router) Available() bool { return r.cfg.Enabled }

// Engine is what routes while routing is on: EngineBasic, or EngineJev
// when Jev is configured (Basic still serves whenever Jev can't answer).
func (r *Router) Engine() string {
	if r.jev != nil {
		return EngineJev
	}
	return EngineBasic
}

// Auto reports whether Jev picks the orchestrator's model.
func (r *Router) Auto() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auto
}

// SubagentRouting reports whether subagent briefs and side tasks are routed
// rather than run on the orchestrator's model.
func (r *Router) SubagentRouting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subagents
}

// Mode returns the routing mode.
func (r *Router) Mode() Mode {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.auto:
		return ModeAuto
	case r.subagents:
		return ModePinned
	}
	return ModeOff
}

// SetMode switches the routing mode. Entering auto forgets the sticky
// orchestrator so the next turn decides afresh; turning routing off resets
// the objective to auto. Without routing available the mode stays off.
func (r *Router) SetMode(mode Mode) {
	if !r.Available() {
		mode = ModeOff
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auto = mode == ModeAuto
	r.subagents = mode == ModeAuto || mode == ModePinned
	if r.auto {
		r.orch, r.cold = nil, true
	}
	if !r.auto {
		r.objective = ObjectiveAuto
	}
	if !r.subagents {
		r.subObjective = ObjectiveAuto
	}
}

// PinOrchestrator records that the user picked the orchestrator's model:
// the router stops picking it. The subagents' choice does not change.
func (r *Router) PinOrchestrator() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auto, r.objective = false, ObjectiveAuto
}

// MarkCold records that the orchestrator's prompt cache no longer helps
// (after compaction), so the next turn may pick any model.
func (r *Router) MarkCold() {
	r.mu.Lock()
	r.cold = true
	r.mu.Unlock()
}

// Adopt makes d the sticky orchestrator decision, as after a failover.
func (r *Router) Adopt(d *Decision) {
	r.mu.Lock()
	r.orch = d
	r.mu.Unlock()
}

// Pin restricts routing to one tier until Pin("") clears it.
func (r *Router) Pin(tier string) error {
	if tier != "" && r.cfg.tierIndex(tier) < 0 {
		return fmt.Errorf("router: unknown tier %q", tier)
	}
	r.mu.Lock()
	r.pinned = tier
	r.cold = true
	r.mu.Unlock()
	return nil
}

// Pinned returns the pinned tier, or "".
func (r *Router) Pinned() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pinned
}

// Last returns the most recent decision.
func (r *Router) Last() *Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Stats returns a copy of the counters.
func (r *Router) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// errNoClassifier is Classify's error when routing has no Jev endpoint.
var errNoClassifier = errors.New("router: model routing needs a Jev endpoint")

// Classify asks Jev about a prompt without choosing a model. There is no
// guess when Jev can't answer: the error means routing pauses.
func (r *Router) Classify(ctx context.Context, in Input) (Classification, error) {
	if r.jev == nil {
		return Classification{}, errNoClassifier
	}
	label := contextLabel(in.ContextTokens)
	redacted, secret := ScanSecrets(in.Prompt)
	class, err := r.jev.classify(ctx, redacted, label)
	r.mu.Lock()
	r.stats.JevCalls++
	if err != nil {
		r.stats.JevFailures++
	} else {
		r.stats.JevCost += class.Cost
	}
	r.mu.Unlock()
	r.jevAnswered(err)
	if err != nil {
		return Classification{}, err
	}
	if secret {
		class.Sensitive = 1
	}
	return class, nil
}

// jevAnswered tracks whether Jev is answering. The first failure after a
// success queues one notice (see TakePauseNotice); until Jev answers again
// the Basic rules route.
func (r *Router) jevAnswered(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.paused = false
	case !r.paused:
		r.paused = true
		r.pauseNotice = "Jev unreachable (" + strings.TrimPrefix(err.Error(), "router: ") + ") — routing by Basic rules until it answers."
	}
}

// TakePauseNotice returns the notice queued when Jev stopped answering,
// once.
func (r *Router) TakePauseNotice() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	notice := r.pauseNotice
	r.pauseNotice = ""
	return notice
}

// Decide chooses the orchestrator's model for a new turn. It returns nil
// when the router does not pick the orchestrator (a model is pinned) or no
// configured model is eligible (see NoRouteReason).
//
// Jev routes when it is the engine and answers; otherwise the Basic rules
// do (see chooseBasic). Basic never moves off a warm model that is still
// eligible.
//
// The choice is sticky: the first turn of a session decides freely, and so
// does the first turn after the cache went cold (StickyIdleMinutes without a
// request, a compaction, a mode or pin change, or the model becoming
// unusable). While the cache is warm a turn may only move to a more capable
// model, never down, because a switch re-prefills the whole conversation.
// When no usable model is stronger than the sticky one, the turn keeps it
// without asking Jev.
func (r *Router) Decide(ctx context.Context, in Input) *Decision {
	r.mu.Lock()
	auto, cur, cold, active := r.auto, r.orch, r.cold, r.active
	now := r.now()
	r.mu.Unlock()
	if !auto {
		return nil
	}
	if idle := r.stickyIdle(); cur != nil && !active.IsZero() && now.Sub(active) >= idle {
		cold = true
	}
	var curCand candidate
	if cur != nil && !cold {
		c, ok := r.usable(cur, in.ContextTokens)
		// A plan nearly out moves a warm orchestrator now, not at the next
		// re-pick.
		if !ok || r.quotaCritical(c) {
			cold = true
		}
		curCand = c
	}
	if cur != nil && !cold && (r.jev == nil || !r.strongerExists(curCand, in.ContextTokens)) {
		why := "sticky: no stronger model"
		if r.jev == nil {
			why = "basic: sticky"
		}
		return r.keep(cur, curCand, in.ContextTokens, cur.Class, why)
	}
	if r.jev == nil {
		return r.decideBasic(in.ContextTokens, now)
	}
	class, err := r.Classify(ctx, in)
	if err != nil {
		// Jev can't answer: the Basic rules route this turn, and a warm
		// model stays.
		if cur != nil && !cold {
			return r.keep(cur, curCand, in.ContextTokens, cur.Class, "Jev unreachable: sticky")
		}
		return r.decideBasic(in.ContextTokens, now)
	}
	fresh := r.decideWith(class, in.ContextTokens)
	if cur == nil || cold || fresh == nil {
		// A fresh start may compact to fit a better model; a warm cache is
		// kept whole.
		if alt := r.compactAlternative(class, in, fresh); alt != nil {
			fresh = alt
		}
	}
	if fresh == nil {
		return nil
	}
	if cur == nil || cold || fresh.CompactToFit {
		r.mu.Lock()
		r.orch, r.cold, r.active = fresh, false, now
		r.mu.Unlock()
		return fresh
	}
	if fresh.need > r.capabilityOf(curCand)+r.cfg.Slack && r.capabilityOf(fresh.candidate()) > r.capabilityOf(curCand) {
		fresh.Reason = "ratchet up from " + cur.Spec() + ": " + fresh.Reason
		r.mu.Lock()
		r.orch, r.active = fresh, now
		r.mu.Unlock()
		return fresh
	}
	return r.keep(cur, curCand, in.ContextTokens, class, "sticky: warm cache")
}

// keep repeats the sticky decision for a new turn. Thinking may rise with
// the new classification but never falls mid-session.
func (r *Router) keep(cur *Decision, c candidate, contextTokens int, class Classification, why string) *Decision {
	tier := r.cfg.Tiers[c.tier]
	thinking := cur.Thinking
	if !cur.Basic {
		thinking = r.thinkingFor(class, class.Demand(), tier, c, cur.Objective)
		if rank(cur.Thinking) > rank(thinking) {
			thinking = cur.Thinking
		}
	}
	out := *cur
	out.Thinking = thinking
	out.Fallback, out.CompactToFit, out.whole = false, false, nil
	out.ContextTokens = contextTokens
	out.At = r.now()
	out.Reason = why + " on " + cur.Spec()
	r.mu.Lock()
	r.orch, r.last, r.active = &out, &out, out.At
	r.stats.Decisions++
	r.mu.Unlock()
	return &out
}

func (r *Router) stickyIdle() time.Duration {
	if r.cfg.StickyIdleMinutes > 0 {
		return time.Duration(r.cfg.StickyIdleMinutes) * time.Minute
	}
	return 10 * time.Minute
}

// usable reports whether the decision's model can still serve contextTokens.
func (r *Router) usable(d *Decision, contextTokens int) (candidate, bool) {
	idx := r.cfg.tierIndex(d.Tier)
	if idx < 0 {
		return candidate{}, false
	}
	for _, c := range r.tierCandidates(idx, contextTokens, r.Objective()) {
		if c.ref.Spec() == d.Spec() {
			return c, true
		}
	}
	return candidate{}, false
}

// strongerExists reports whether a usable model outranks c in capability.
func (r *Router) strongerExists(c candidate, contextTokens int) bool {
	for _, other := range r.allCandidates(contextTokens, r.Objective()) {
		if r.cfg.Tiers[other.tier].Cost == CostPaid && !r.cfg.PaidLastResort {
			continue
		}
		if r.capabilityOf(other) > r.capabilityOf(c) {
			return true
		}
	}
	return false
}

// decideWith chooses a model for class and records the decision.
func (r *Router) decideWith(class Classification, contextTokens int) *Decision {
	d := r.choose(class, contextTokens)
	if d != nil {
		r.mu.Lock()
		r.last = d
		r.stats.Decisions++
		r.mu.Unlock()
	}
	return d
}

// choose picks a model for class among those that fit contextTokens.
func (r *Router) choose(class Classification, contextTokens int) *Decision {
	demand := class.Demand()
	r.mu.Lock()
	pinned, objective := r.pinned, r.objective
	r.mu.Unlock()

	// Cost mode goes by the prompt's own demand, less costTolerance: kind
	// floors are preferences for going up when unsure, which cost does not
	// pay for.
	cost := objective == ObjectiveCost
	need := demand
	if policy, ok := r.cfg.Kinds[string(class.Kind)]; ok && policy.MinCapability > need && !cost {
		need = policy.MinCapability
	}
	// Sensitive prompts drop the kind floor so they can stay on owned
	// hardware whenever an owned model is capable enough.
	private := r.cfg.SensitiveStaysPrivate && class.Sensitive >= 0.6
	if private {
		need = demand
	}
	if cost {
		need = max(0, need-costTolerance)
	}
	paidByKind := slices.Contains(r.cfg.PaidKinds, string(class.Kind))

	var chain []candidate
	reason := ""
	switch {
	case pinned != "":
		chain = r.rank(r.tierCandidates(r.cfg.tierIndex(pinned), contextTokens, objective), 0, contextTokens, true, objective)
		reason = "pinned to " + pinned
	case objective == ObjectiveQuality:
		// Quality ignores need and cost short of pay-per-token: any model
		// near the strongest reachable one.
		chain = r.rank(r.band(r.allCandidates(contextTokens, objective), nil), 0, contextTokens, true, objective)
	default:
		pool := r.candidates(contextTokens, paidByKind, objective)
		chain = r.adequate(r.rank(pool, need, contextTokens, true, objective), need)
		if len(chain) == 0 && need > demand {
			// The kind floor is a preference; before paying, see whether an
			// unpaid model meets the prompt's actual demand.
			chain = r.adequate(r.rank(pool, demand, contextTokens, true, objective), demand)
			if len(chain) > 0 {
				reason = fmt.Sprintf("%s floor %.2f unavailable; meets demand %.2f", class.Kind, need, demand)
			}
		}
		if len(chain) == 0 {
			// Nothing is capable enough: take the most capable model that
			// fits, paid only when allowed.
			chain = r.rank(r.candidates(contextTokens, r.cfg.PaidLastResort, objective), need, contextTokens, true, objective)
			if len(chain) > 0 {
				reason = fmt.Sprintf("no model meets need %.2f; most capable that fits", need)
			}
		}
	}
	if len(chain) == 0 {
		return nil
	}
	quota := ""
	if pinned == "" {
		chain, quota = r.balanceQuota(chain, need)
	}
	chosen := chain[0]
	tier := r.cfg.Tiers[chosen.tier]
	if reason == "" {
		reason = fmt.Sprintf("%s · need %.2f · cap %.2f", class.Kind, need, r.capabilityOf(chosen))
		if eta, ok := r.expectedTTFT(chosen, contextTokens); ok {
			reason += fmt.Sprintf(" · ~%.1fs to first token", eta)
		}
		reason += fmt.Sprintf(" · %s tokens", humanTokens(contextTokens))
		if private {
			reason += " · private"
		}
	}
	if quota != "" {
		reason = quota + " · " + reason
	}
	reason += objectiveNote(objective)
	d := &Decision{
		Tier:          tier.Name,
		Provider:      chosen.ref.Provider,
		Model:         chosen.ref.Model,
		Display:       chosen.info.DisplayName,
		Thinking:      r.thinkingFor(class, demand, tier, chosen, objective),
		Class:         class,
		Demand:        demand,
		need:          need,
		Reason:        reason,
		Objective:     objective,
		ContextTokens: contextTokens,
		At:            r.now(),
		chosen:        chosen,
		chain:         chain[1:],
	}
	return d
}

// costRank orders cost classes: owned hardware, then subscription, then
// pay-per-token.
func costRank(cost string) int {
	switch cost {
	case CostFreeLocal, CostFreeRemote:
		return 0
	case CostSubscription:
		return 1
	default:
		return 2
	}
}

func (r *Router) capabilityOf(c candidate) float64 {
	if c.ref.Capability > 0 {
		return c.ref.Capability
	}
	return r.cfg.Tiers[c.tier].Capability
}

func (r *Router) isAdequate(c candidate, need float64) bool {
	return r.capabilityOf(c)+r.cfg.Slack >= need
}

// expectedTTFT estimates seconds to first token. The model that served the
// previous request only has to prefill what was added since, which makes
// staying on it cheap while its cache is warm.
func (r *Router) expectedTTFT(c candidate, contextTokens int) (float64, bool) {
	uncached := contextTokens
	r.mu.Lock()
	if r.lastSpec == c.key() && r.lastTokens > 0 && r.lastTokens <= contextTokens {
		uncached = contextTokens - r.lastTokens
	}
	r.mu.Unlock()
	return r.speed.estimate(c.key(), uncached)
}

// rank orders candidates: adequate before inadequate; adequate ones by cost
// class, then expected time to first token (unmeasured models count as
// instant so they get measured), then capability (strongest first when
// preferStrong, else weakest, which saves scarce quota on side tasks);
// inadequate ones by capability. A fast model that is not capable enough
// never outranks a capable one. Speed and quality mode ignore cost short of
// pay-per-token and count unmeasured models as slow. Cost mode orders
// adequate subscription models by the quota their plan has left, then the
// least capability that suffices, before speed.
func (r *Router) rank(cands []candidate, need float64, contextTokens int, preferStrong bool, o Objective) []candidate {
	fast := o == ObjectiveSpeed || o == ObjectiveQuality
	eta := r.etas(cands, contextTokens, fast)
	var quota map[string]int
	if o == ObjectiveCost {
		quota = r.quotaBands(cands)
	}
	out := slices.Clone(cands)
	slices.SortStableFunc(out, func(a, b candidate) int {
		aOK, bOK := r.isAdequate(a, need), r.isAdequate(b, need)
		switch {
		case aOK && !bOK:
			return -1
		case !aOK && bOK:
			return 1
		case !aOK && !bOK:
			return cmpFloatDesc(r.capabilityOf(a), r.capabilityOf(b))
		}
		if ra, rb := costRank(r.cfg.Tiers[a.tier].Cost), costRank(r.cfg.Tiers[b.tier].Cost); ra != rb && (!fast || max(ra, rb) == costRank(CostPaid)) {
			return ra - rb
		}
		if quota != nil && r.cfg.Tiers[a.tier].Cost == CostSubscription {
			if c := cmp.Or(cmp.Compare(quota[b.key()], quota[a.key()]), cmp.Compare(r.capabilityOf(a), r.capabilityOf(b))); c != 0 {
				return c
			}
		}
		if c := cmp.Compare(eta[a.key()], eta[b.key()]); c != 0 {
			return c
		}
		if preferStrong {
			return cmpFloatDesc(r.capabilityOf(a), r.capabilityOf(b))
		}
		return cmpFloatDesc(r.capabilityOf(b), r.capabilityOf(a))
	})
	return out
}

func cmpFloatDesc(a, b float64) int { return cmp.Compare(b, a) }

// adequate returns the leading adequate candidates of a ranked list.
func (r *Router) adequate(ranked []candidate, need float64) []candidate {
	for i, c := range ranked {
		if !r.isAdequate(c, need) {
			return ranked[:i]
		}
	}
	return ranked
}

// allCandidates returns every usable model that fits, in config order.
func (r *Router) allCandidates(contextTokens int, o Objective) []candidate {
	var out []candidate
	for i := range r.cfg.Tiers {
		out = append(out, r.tierCandidates(i, contextTokens, o)...)
	}
	return out
}

// anyCandidates returns usable models that fit, ranked for need 0 (cost,
// then speed), paid only when allowed.
func (r *Router) anyCandidates(contextTokens int, allowPaid bool, o Objective) []candidate {
	return r.rank(r.candidates(contextTokens, allowPaid, o), 0, contextTokens, false, ObjectiveAuto)
}

// candidates returns every usable model that fits, in config order, leaving
// out pay-per-token tiers unless allowPaid.
func (r *Router) candidates(contextTokens int, allowPaid bool, o Objective) []candidate {
	return filter(r.allCandidates(contextTokens, o), func(c candidate) bool {
		return allowPaid || r.cfg.Tiers[c.tier].Cost != CostPaid
	})
}

// Observe records one served request: its total context, the part that had
// to be prefilled, and the time to its first streamed token.
func (r *Router) Observe(spec string, contextTokens, uncachedTokens int, ttft time.Duration) {
	r.speed.observe(spec, uncachedTokens, ttft)
	r.mu.Lock()
	r.lastSpec, r.lastTokens = spec, contextTokens
	r.served[spec] = true
	if r.orch != nil && r.orch.Spec() == spec {
		r.active = r.now()
	}
	r.mu.Unlock()
}

// Target health states reported by Targets.
const (
	TargetUnknown = iota
	TargetUp
	TargetDown
)

// TargetStatus is the health and expected speed of one provider in a tier.
type TargetStatus struct {
	Tier     string
	Provider string
	// Health is TargetUp when the tier's probe succeeded or one of the
	// provider's models in the tier served a request, TargetDown when the
	// probe failed or every one of those models is resting after failures,
	// else TargetUnknown.
	Health int
	// TTFT is the fastest expected time to first token among the measured
	// models for the current context; zero when none is measured.
	TTFT time.Duration
}

// Targets reports each tier's providers, in configuration order, from
// cached state; it never probes.
func (r *Router) Targets() []TargetStatus {
	var out []TargetStatus
	var models [][]ModelRef
	r.mu.Lock()
	contextTokens := r.lastTokens
	now := r.now()
	for _, tier := range r.cfg.Tiers {
		first := len(out)
		for _, ref := range tier.Models {
			i := slices.IndexFunc(out[first:], func(t TargetStatus) bool { return t.Provider == ref.Provider })
			if i < 0 {
				out = append(out, TargetStatus{Tier: tier.Name, Provider: ref.Provider})
				models = append(models, nil)
				i = len(out) - 1 - first
			}
			models[first+i] = append(models[first+i], ref)
		}
		probe, probed := r.probes[tier.Name]
		for i := first; i < len(out); i++ {
			resting := true
			for _, ref := range models[i] {
				if until, ok := r.down[ref.Spec()]; !ok || !until.After(now) {
					resting = false
				}
				if r.served[ref.Spec()] {
					out[i].Health = TargetUp
				}
			}
			if probed && tier.ProbeURL != "" {
				out[i].Health = TargetDown
				if probe.ok {
					out[i].Health = TargetUp
				}
			}
			if resting {
				out[i].Health = TargetDown
			}
		}
	}
	r.mu.Unlock()
	for i, refs := range models {
		for _, ref := range refs {
			if seconds, ok := r.expectedTTFT(candidate{ref: ref}, contextTokens); ok {
				if d := time.Duration(seconds * float64(time.Second)); out[i].TTFT == 0 || d < out[i].TTFT {
					out[i].TTFT = d
				}
			}
		}
	}
	return out
}

// tierCandidates returns the tier's usable models in chain order.
func (r *Router) tierCandidates(idx int, contextTokens int, o Objective) []candidate {
	if idx < 0 || idx >= len(r.cfg.Tiers) {
		return nil
	}
	tier := r.cfg.Tiers[idx]
	if !r.probe(tier) {
		return nil
	}
	var out []candidate
	for _, ref := range tier.Models {
		if r.isDown(ref.Spec()) || !r.allowed(tier, ref, o) {
			continue
		}
		info, ok := r.host.ModelInfo(ref.Provider, ref.Model)
		if !ok {
			continue
		}
		if !r.fits(tier, info, contextTokens) {
			continue
		}
		out = append(out, candidate{tier: idx, ref: ref, info: info})
	}
	return out
}

// fits reports whether a conversation of contextTokens fits the model
// without compacting: the rule compaction uses (ai.UsableContext), so a
// routed model never starts out needing a compaction. The window is the
// model's as configured, which may be a limit the user chose.
func (r *Router) fits(tier TierConfig, info ModelInfo, contextTokens int) bool {
	usable := ai.UsableContext(info.ContextWindow, info.MaxOutputTokens)
	if tier.ContextShare > 0 && tier.ContextShare < 1 && info.ContextWindow > 0 {
		usable = min(usable, int(float64(info.ContextWindow)*tier.ContextShare))
	}
	return contextTokens <= usable
}

func (r *Router) isDown(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.down[key]
	if !ok {
		return false
	}
	if r.now().After(until) {
		delete(r.down, key)
		return false
	}
	return true
}

func (r *Router) markDown(key string, d time.Duration) {
	r.mu.Lock()
	r.down[key] = r.now().Add(d)
	r.mu.Unlock()
}

// probe checks a tier's endpoint, caching the answer for the probe interval.
func (r *Router) probe(tier TierConfig) bool {
	if tier.ProbeURL == "" {
		return true
	}
	interval := time.Duration(r.cfg.ProbeIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	r.mu.Lock()
	cached, ok := r.probes[tier.Name]
	now := r.now()
	r.mu.Unlock()
	if ok && now.Sub(cached.at) < interval {
		return cached.ok
	}
	result := probeResult{at: now}
	req, err := http.NewRequest(http.MethodGet, tier.ProbeURL, nil)
	if err != nil {
		result.err = err.Error()
	} else {
		resp, err := r.client.Do(req)
		if err != nil {
			result.err = err.Error()
		} else {
			_ = resp.Body.Close()
			result.ok = resp.StatusCode < 500
			if !result.ok {
				result.err = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		}
	}
	r.mu.Lock()
	r.probes[tier.Name] = result
	r.mu.Unlock()
	return result.ok
}

// thinkingFor picks a thinking level from demand and kind, shifted by the
// objective, and capped by the model, the tier, and the candidate's own cap.
func (r *Router) thinkingFor(class Classification, demand float64, tier TierConfig, c candidate, o Objective) ai.ThinkingLevel {
	if c.info.MaxThinking == "" {
		return ai.ThinkingOff
	}
	ladder := []ai.ThinkingLevel{ai.ThinkingOff, ai.ThinkingLow, ai.ThinkingMedium, ai.ThinkingHigh, ai.ThinkingXHigh}
	level := 0
	switch {
	case c.ref.Effort != "":
		level = max(slices.Index(ladder, ai.ThinkingLevel(c.ref.Effort)), 0)
		if ai.ThinkingLevel(c.ref.Effort) == ai.ThinkingMinimal {
			level = 1
		}
	case demand >= 0.7:
		level = 3
	case demand >= 0.5:
		level = 2
	case demand >= 0.3:
		level = 1
	}
	if policy, ok := r.cfg.Kinds[string(class.Kind)]; ok {
		level += policy.ThinkingBoost
	}
	if class.DeepReasoning >= 0.7 && level < 2 {
		level = 2
	}
	// Auto tops out at high; only quality mode reaches xhigh.
	level = min(max(level, 0), 3)
	level = min(max(level+thinkingShift(o), 0), len(ladder)-1)
	chosen := ladder[level]
	for _, cap := range []string{c.ref.Thinking, tier.MaxThinking, string(c.info.MaxThinking)} {
		if cap == "" {
			continue
		}
		if rank(ai.ThinkingLevel(cap)) < rank(chosen) {
			chosen = ai.ThinkingLevel(cap)
		}
	}
	return chosen
}

// thinkingOrder lists the thinking levels from least to most.
var thinkingOrder = []ai.ThinkingLevel{ai.ThinkingOff, ai.ThinkingMinimal, ai.ThinkingLow, ai.ThinkingMedium, ai.ThinkingHigh, ai.ThinkingXHigh}

// rank orders thinking levels: "" counts as off, an unknown level above xhigh.
func rank(level ai.ThinkingLevel) int {
	if level == "" {
		return 0
	}
	if i := slices.Index(thinkingOrder, level); i >= 0 {
		return i
	}
	return len(thinkingOrder)
}

// Next abandons the decision's model after a failure and returns the next
// usable candidate, or nil when none remains.
func (r *Router) Next(d *Decision, failure Failure) *Decision {
	if d == nil {
		return nil
	}
	cooldown := 60 * time.Second
	switch {
	case failure.RateLimited:
		cooldown = time.Duration(r.cfg.QuotaBackoffMinutes) * time.Minute
		if cooldown <= 0 {
			cooldown = 15 * time.Minute
		}
	case failure.Overflow:
		// Not a health problem: only skip it for this context size.
		cooldown = 0
	case strings.Contains(strings.ToLower(failure.Message), "not configured"):
		cooldown = 10 * time.Minute
	}
	if cooldown > 0 {
		r.markDown(d.Spec(), cooldown)
	}
	tokens := failure.ContextTokens
	if tokens <= 0 {
		tokens = d.ContextTokens
	}
	needWindow := 0
	if failure.Overflow {
		if info, ok := r.host.ModelInfo(d.Provider, d.Model); ok {
			needWindow = info.ContextWindow
		}
	}
	// The mode now in force governs, even for a chain decided before a
	// switch: the subagents' for a brief, else the orchestrator's.
	current := r.Objective()
	if d.Brief != nil {
		current = r.SubagentObjective()
	}
	pick := func(chain []candidate) *candidate {
		for _, c := range chain {
			if c.ref.Spec() == d.Spec() || r.isDown(c.key()) || !r.allowed(r.cfg.Tiers[c.tier], c.ref, current) {
				continue
			}
			info, ok := r.host.ModelInfo(c.ref.Provider, c.ref.Model)
			if !ok {
				continue
			}
			c.info = info
			if needWindow > 0 && info.ContextWindow > 0 && info.ContextWindow <= needWindow {
				continue
			}
			if !r.fits(r.cfg.Tiers[c.tier], info, tokens) {
				continue
			}
			return &c
		}
		return nil
	}
	next := pick(d.chain)
	remaining := d.chain
	if next == nil {
		// The chain is exhausted: widen to every model, capable ones first,
		// keeping the last-resort rule for paid tiers.
		pool := r.candidates(tokens, r.cfg.PaidLastResort, current)
		if d.Basic {
			pool = slices.DeleteFunc(pool, func(c candidate) bool { return c.ref.Spec() == d.Spec() })
		} else if d.Objective == ObjectiveQuality {
			// The band slides down to the strongest model still reachable.
			pool = r.band(slices.DeleteFunc(pool, func(c candidate) bool { return c.ref.Spec() == d.Spec() }), nil)
		}
		if d.Basic {
			remaining = r.basicOrder(pool, tokens, current)
		} else {
			remaining = r.rank(pool, d.need, tokens, true, d.Objective)
		}
		next = pick(remaining)
	}
	if next == nil {
		return nil
	}
	pos := slices.IndexFunc(remaining, func(c candidate) bool { return c.ref.Spec() == next.ref.Spec() })
	tier := r.cfg.Tiers[next.tier]
	reason := fmt.Sprintf("after %s failed: %s", d.Spec(), text.Clip(strings.TrimSpace(failure.Message), 120))
	if failure.Overflow {
		reason = fmt.Sprintf("after %s overflowed at %s tokens", d.Spec(), humanTokens(tokens))
	}
	reason += objectiveNote(d.Objective)
	thinking := ai.ThinkingLevel(next.ref.Effort)
	if !d.Basic {
		thinking = r.thinkingFor(d.Class, d.Demand, tier, *next, d.Objective)
	}
	out := &Decision{
		Tier:          tier.Name,
		Provider:      next.ref.Provider,
		Model:         next.ref.Model,
		Display:       next.info.DisplayName,
		Thinking:      thinking,
		Basic:         d.Basic,
		Brief:         d.Brief,
		Class:         d.Class,
		Demand:        d.Demand,
		need:          d.need,
		Reason:        reason,
		Objective:     d.Objective,
		Fallback:      true,
		ContextTokens: tokens,
		At:            r.now(),
		chosen:        *next,
	}
	if pos >= 0 && pos+1 <= len(remaining) {
		out.chain = remaining[pos+1:]
	}
	r.mu.Lock()
	r.last = out
	r.stats.Decisions++
	r.stats.Fallbacks++
	r.mu.Unlock()
	return out
}

// SideTask picks the cheapest usable model that fits contextTokens for
// background work such as compaction summaries. It never uses a paid tier
// unless PaidLastResort is set and nothing else fits. Under the Basic rules
// the cheapest cost class wins, then the highest-ranked model in it.
func (r *Router) SideTask(contextTokens int) (ModelRef, ModelInfo, bool) {
	if !r.SubagentRouting() {
		return ModelRef{}, ModelInfo{}, false
	}
	o := r.SubagentObjective()
	if r.jev == nil {
		chain := r.basicSideOrder(r.candidates(contextTokens, r.cfg.PaidLastResort, o))
		if len(chain) == 0 {
			return ModelRef{}, ModelInfo{}, false
		}
		return chain[0].ref, chain[0].info, true
	}
	chain := r.anyCandidates(contextTokens, false, o)
	if len(chain) == 0 && r.cfg.PaidLastResort {
		chain = r.anyCandidates(contextTokens, true, o)
	}
	if len(chain) == 0 {
		return ModelRef{}, ModelInfo{}, false
	}
	return chain[0].ref, chain[0].info, true
}

// Status renders the router state for /router.
func (r *Router) Status() string {
	r.mu.Lock()
	pinned, last, stats, orch := r.pinned, r.last, r.stats, r.orch
	auto, subs, objective, subObjective := r.auto, r.subagents, r.objective, r.subObjective
	probes, down := maps.Clone(r.probes), maps.Clone(r.down)
	now := r.now()
	r.mu.Unlock()
	speeds := r.speed.snapshot()

	var b strings.Builder
	orchestrator, subagents := "your model", "not routed (a chosen model, or the orchestrator's)"
	if auto {
		orchestrator = string(objective) + " (sticky while the cache is warm)"
		if orch != nil {
			orchestrator += " · now " + orch.Spec()
		}
	}
	if subs {
		subagents = string(subObjective) + " (routed per brief)"
	}
	fmt.Fprintf(&b, "**Router:** orchestrator: %s · subagents: %s", orchestrator, subagents)
	if pinned != "" {
		fmt.Fprintf(&b, " · pinned to tier %s", pinned)
	}
	switch {
	case !r.cfg.Enabled:
		b.WriteString(" · off (turn it on in /setup → Model routing)")
	case r.jev == nil:
		b.WriteString(" · engine: Basic rules")
	default:
		fmt.Fprintf(&b, " · engine: Jev (%s)", r.cfg.Jev.Model)
		if open := r.jev.breakerRemaining(); open > 0 {
			fmt.Fprintf(&b, " (unreachable, retrying in %s; the Basic rules route meanwhile)", open.Round(time.Second))
		}
	}
	b.WriteString("\n\n")
	for _, tier := range r.cfg.Tiers {
		health := ""
		if p, ok := probes[tier.Name]; ok && tier.ProbeURL != "" {
			if p.ok {
				health = " · up"
			} else {
				health = " · down (" + p.err + ")"
			}
		}
		fmt.Fprintf(&b, "- **%s** (%s)%s\n", tier.Name, tier.Cost, health)
		for _, ref := range tier.Models {
			capability := cmp.Or(ref.Capability, tier.Capability)
			speed := "speed not measured yet"
			if st, ok := speeds[ref.Spec()]; ok && st.Samples > 0 {
				speed = fmt.Sprintf("~%.1fs + %.2fs per 1K new tokens (%d samples)", st.BaseSeconds, st.PerKSeconds, st.Samples)
			}
			mark := ""
			if until, ok := down[ref.Spec()]; ok && until.After(now) {
				mark = fmt.Sprintf(" · resting %s", until.Sub(now).Round(time.Second))
			} else if _, ok := r.host.ModelInfo(ref.Provider, ref.Model); !ok {
				mark = " · not configured"
			}
			fmt.Fprintf(&b, "  - %s · cap %.2f · %s%s\n", ref.Spec(), capability, speed, mark)
		}
	}
	if last != nil {
		fmt.Fprintf(&b, "\n**Last:** %s\n%s\n", last.Summary(), last.Reason)
		fmt.Fprintf(&b, "kind %s (%.2f) · complexity %.2f · capability %.2f · deep %.2f · sensitive %.2f · via %s\n",
			last.Class.Kind, last.Class.KindConfidence, last.Class.Complexity, last.Class.Capability, last.Class.DeepReasoning, last.Class.Sensitive, last.Class.Source)
	}
	fmt.Fprintf(&b, "\ndecisions %d · subagent briefs %d · fallbacks %d · jev calls %d (%d failed, $%.5f)\n", stats.Decisions, stats.Briefs, stats.Fallbacks, stats.JevCalls, stats.JevFailures, stats.JevCost)
	return b.String()
}

func contextLabel(tokens int) string {
	switch {
	case tokens < 4000:
		return "short: the conversation just started"
	case tokens < 30000:
		return "medium: some files and results have been read"
	case tokens < 100000:
		return "long: many files, tool results, and turns so far"
	default:
		return "very long: a large amount of code and history is loaded"
	}
}

func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%dK", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
