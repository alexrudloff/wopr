package router

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// Quota balancing: when the plan behind the top pick is getting tight, a
// similarly ranked model on another subscription plan with clearly more
// allowance left takes the work instead. It applies to orchestrator and
// subagent picks, under Basic and Jev routing; a pinned orchestrator tier
// is left alone.
//
//	tight     a window has under quotaTight% left, or its use runs more than
//	          quotaPace points ahead of the time elapsed in it
//	critical  a window has under quotaCritical% left: a warm orchestrator
//	          moves at the next turn instead of waiting for a re-pick
//	peer      a subscription model on another plan within one rank step
//	switch    the peer with the most left, when that is at least
//	          quotaMargin points more than the top pick's plan has
//
// A plan that has not reported usage is not tight and counts as
// unknownQuota left.
const (
	quotaTight    = 30.0
	quotaPace     = 15.0
	quotaCritical = 10.0
	quotaMargin   = 10.0
	// rankRounding absorbs strengths being rounded to two decimals.
	rankRounding = 0.01
)

// planState is a subscription plan's latest usage as quota balancing sees
// it.
type planState struct {
	plan string
	// left is the allowance left in the tightest unexpired window, 0..100;
	// window names that window, "" when the plan hasn't reported.
	left     float64
	window   string
	tight    bool
	critical bool
}

// SetQuotaBalance turns quota balancing on or off.
func (r *Router) SetQuotaBalance(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.quotaBalance = on
}

func (r *Router) quotaBalanceOn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quotaBalance
}

// planOf reports the usage of the subscription plan behind c; ok is false
// when c is not a subscription model with a known plan.
func (r *Router) planOf(c candidate) (planState, bool) {
	plan, known := subscriptionPlans[c.ref.Provider]
	if !known || r.cfg.Tiers[c.tier].Cost != CostSubscription {
		return planState{}, false
	}
	now := r.now()
	s := planState{plan: plan, left: unknownQuota}
	for _, p := range r.usage() {
		if p.Plan != plan {
			continue
		}
		s.left = 100
		for _, w := range p.Windows {
			if !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
				continue
			}
			left := 100 - w.UsedPercent
			if left < s.left {
				s.left, s.window = left, w.Label
			}
			if left < quotaTight || aheadOfPace(w, now) {
				s.tight = true
			}
			if left < quotaCritical {
				s.critical = true
			}
		}
	}
	return s, true
}

// aheadOfPace reports whether a window's use runs more than quotaPace
// points ahead of the share of the window that has elapsed. It needs the
// window's length and reset time.
func aheadOfPace(w ai.UsageWindow, now time.Time) bool {
	length := windowLength(w.Label)
	if length <= 0 || w.ResetsAt.IsZero() {
		return false
	}
	elapsed := 100 * (1 - float64(w.ResetsAt.Sub(now))/float64(length))
	return w.UsedPercent-max(0, elapsed) > quotaPace
}

// windowLength parses a usage window label ("5h", "7d", "wk", "90m").
func windowLength(label string) time.Duration {
	if label == "wk" {
		return 7 * 24 * time.Hour
	}
	if len(label) < 2 {
		return 0
	}
	n, err := strconv.Atoi(label[:len(label)-1])
	if err != nil || n <= 0 {
		return 0
	}
	switch label[len(label)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	}
	return 0
}

// rankStep is the strength between neighbouring positions in the ranking
// (every routed model when the configuration has none), or 0 when fewer
// than two models are ranked.
func (r *Router) rankStep() float64 {
	n := len(r.cfg.Ranking)
	if n == 0 {
		for _, tier := range r.cfg.Tiers {
			n += len(tier.Models)
		}
	}
	if n < 2 {
		return 0
	}
	return (rankHigh - rankLow) / float64(n-1)
}

// balanceQuota moves a peer to the head of chain when the head's plan is
// tight, returning the reordered chain and a note for the decision's
// reason ("" when nothing moved). Peers must meet need.
func (r *Router) balanceQuota(chain []candidate, need float64) ([]candidate, string) {
	if len(chain) < 2 || !r.quotaBalanceOn() {
		return chain, ""
	}
	head, ok := r.planOf(chain[0])
	step := r.rankStep()
	if !ok || !head.tight || step <= 0 {
		return chain, ""
	}
	top := r.capabilityOf(chain[0])
	best, bestState := -1, planState{}
	for i, c := range chain[1:] {
		s, ok := r.planOf(c)
		gap := top - r.capabilityOf(c)
		if !ok || s.plan == head.plan || gap > step+rankRounding || -gap > step+rankRounding || !r.isAdequate(c, need) {
			continue
		}
		if s.left >= head.left+quotaMargin && (best < 0 || s.left > bestState.left) {
			best, bestState = i+1, s
		}
	}
	if best < 0 {
		return chain, ""
	}
	out := append([]candidate{chain[best]}, chain[:best]...)
	out = append(out, chain[best+1:]...)
	return out, fmt.Sprintf("quota: %s → %s", planNote(head), planNote(bestState))
}

// quotaCritical reports whether a warm orchestrator on c should move at
// the next turn because its plan is nearly out. A pinned tier stays.
func (r *Router) quotaCritical(c candidate) bool {
	if !r.quotaBalanceOn() || r.Pinned() != "" {
		return false
	}
	s, ok := r.planOf(c)
	return ok && s.critical
}

// planNote describes a plan's tightest window: "Claude 5h 82% used".
func planNote(s planState) string {
	if s.window == "" {
		return s.plan + " (usage unknown)"
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %.0f%% used", s.plan, s.window, 100-s.left))
}
