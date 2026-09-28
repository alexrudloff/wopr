package efficiency

import "math"

// Online Context Compact economics. A compaction is a prompt-cache write of
// the whole context that pays back over the remaining requests through a
// smaller prompt.

// Compaction cost model.
const (
	// windowReserveTokens is the headroom below the window at which a
	// boundary compacts regardless of economics.
	windowReserveTokens = 16384
	// firstCompactionRequestScale stretches the horizon for the first
	// compaction of a session.
	firstCompactionRequestScale = 2
	// subsequentCompactionMargin is the stricter payback margin after it.
	subsequentCompactionMargin = 1.5
)

// CompactionReason explains a decision.
type CompactionReason string

const (
	ReasonEconomic                 CompactionReason = "economic"
	ReasonWindowProtection         CompactionReason = "window_protection"
	ReasonDeferredEconomic         CompactionReason = "deferred_economic"
	ReasonDeferredSubsequentMargin CompactionReason = "deferred_subsequent_margin"
	ReasonDeferredCarriedDebt      CompactionReason = "deferred_carried_debt"
	ReasonHorizonUnavailable       CompactionReason = "horizon_unavailable"
	ReasonCacheRatioUnavailable    CompactionReason = "cache_ratio_unavailable"
	ReasonNonPositiveSaving        CompactionReason = "non_positive_saving"
)

// RequestHorizon estimates how many provider requests remain.
type RequestHorizon struct {
	WindowRequestUpperBound   int // -1 when unknown
	ExpectedRemainingRequests int
}

// EstimateRemainingRequests projects the remaining request count from the
// mean number of requests past work items took, bounded by how many more
// requests fit in the window at the observed growth rate.
func EstimateRemainingRequests(completedBoundaryRequestCounts []int, remainingBoundaries int, contextTokens, contextWindowTokens int, averageContextTokenIncrement float64) RequestHorizon {
	sum := 0
	for _, c := range completedBoundaryRequestCounts {
		sum += c
	}
	mean := float64(sum) / math.Max(1, float64(len(completedBoundaryRequestCounts)))
	expected := 1 + int(math.Floor(mean*math.Max(0, float64(remainingBoundaries))))
	upper := -1
	if contextWindowTokens > 0 && averageContextTokenIncrement > 0 {
		upper = int(math.Max(0, math.Floor(float64(contextWindowTokens-contextTokens)/averageContextTokenIncrement)))
	}
	if upper >= 0 && upper < expected {
		expected = upper
	}
	return RequestHorizon{WindowRequestUpperBound: upper, ExpectedRemainingRequests: expected}
}

// CompactionInput is what the decision needs.
type CompactionInput struct {
	WriteTokens   int // the whole context that a compaction rewrites
	ArchiveTokens int // tokens that leave the context
	MemoTokens    int // tokens the summary adds
	ContextTokens int
	// CompletedBoundaryRequestCounts is nil when no plan step has completed.
	CompletedBoundaryRequestCounts []int
	RemainingBoundaries            int
	AverageContextTokenIncrement   float64 // 0 when unknown
	ContextWindowTokens            int     // 0 when unknown
	PriorCompactionCount           int
	CarriedDebtTokens              float64
	CacheDebtRepaymentTokens       float64
	CacheWriteReadRatio            float64 // negative when unavailable
}

// CompactionDecision is the priced outcome.
type CompactionDecision struct {
	Compact                   bool
	Reason                    CompactionReason
	IncrementalCacheCostRatio float64 // negative when unavailable
}

// DecideCompaction compacts when the window is
// nearly full, or when the cache-write cost pays back within the expected
// remaining requests (with a stricter margin after the first compaction and
// any debt carried from an earlier one).
func DecideCompaction(in CompactionInput) CompactionDecision {
	var horizon *RequestHorizon
	if in.CompletedBoundaryRequestCounts != nil {
		h := EstimateRemainingRequests(in.CompletedBoundaryRequestCounts, in.RemainingBoundaries, in.ContextTokens, in.ContextWindowTokens, in.AverageContextTokenIncrement)
		horizon = &h
	}
	saving := float64(in.ArchiveTokens - in.MemoTokens)
	incremental := -1.0
	if in.CacheWriteReadRatio >= 0 {
		incremental = math.Max(0, in.CacheWriteReadRatio-1)
	}
	breakeven, combined := -1.0, -1.0
	if saving > 0 && incremental >= 0 {
		breakeven = float64(in.WriteTokens) * incremental / saving
		combined = (in.CarriedDebtTokens + float64(in.WriteTokens)*incremental) / saving
	}
	first := in.PriorCompactionCount == 0
	effective := -1.0
	if horizon != nil {
		effective = float64(horizon.ExpectedRemainingRequests)
		if first {
			effective *= firstCompactionRequestScale
			if horizon.WindowRequestUpperBound >= 0 {
				effective = math.Min(effective, float64(horizon.WindowRequestUpperBound))
			}
		}
	}
	windowProtection := in.ContextWindowTokens > 0 && in.ContextTokens >= in.ContextWindowTokens-windowReserveTokens
	baseEconomic := horizon != nil && horizon.ExpectedRemainingRequests > 0 && breakeven >= 0 && breakeven <= float64(horizon.ExpectedRemainingRequests)
	firstEconomic := first && effective > 0 && breakeven >= 0 && breakeven <= effective
	subsequentMarginOpen := !first && horizon != nil && breakeven >= 0 && breakeven*subsequentCompactionMargin <= float64(horizon.ExpectedRemainingRequests)
	carriedDebtGateOpen := !first && horizon != nil && combined >= 0 && combined <= float64(horizon.ExpectedRemainingRequests)
	economic := firstEconomic
	if !first {
		economic = baseEconomic && subsequentMarginOpen && carriedDebtGateOpen
	}
	compressible := saving > 0
	compact := compressible && (windowProtection || economic)

	reason := ReasonDeferredEconomic
	switch {
	case !compressible:
		reason = ReasonNonPositiveSaving
	case windowProtection:
		reason = ReasonWindowProtection
	case economic:
		reason = ReasonEconomic
	case horizon == nil:
		reason = ReasonHorizonUnavailable
	case breakeven < 0:
		reason = ReasonCacheRatioUnavailable
	case !first && baseEconomic && !subsequentMarginOpen:
		reason = ReasonDeferredSubsequentMargin
	case !first && baseEconomic && !carriedDebtGateOpen:
		reason = ReasonDeferredCarriedDebt
	}
	return CompactionDecision{Compact: compact, Reason: reason, IncrementalCacheCostRatio: incremental}
}
