package efficiency

// OnlineState is the persisted Online Context Compact state. The plan itself
// is the session's work queue; this records the request and compaction
// history the economics need.
type OnlineState struct {
	Version                        int     `json:"version"`
	Epoch                          int     `json:"epoch"`
	RequestCount                   int     `json:"requestCount"`
	LastBoundaryRequestCount       int     `json:"lastBoundaryRequestCount"`
	CompletedBoundaryRequestCounts []int   `json:"completedBoundaryRequestCounts"`
	LastContextTokens              *int    `json:"lastContextTokens"`
	PositiveContextDeltaTotal      float64 `json:"positiveContextDeltaTotal"`
	PositiveContextDeltaCount      int     `json:"positiveContextDeltaCount"`
	NativeCompactionCount          int     `json:"nativeCompactionCount"`
	CacheDebtTokens                float64 `json:"cacheDebtTokens"`
	CacheDebtRepaymentTokens       float64 `json:"cacheDebtRepaymentTokens"`
}

// InitialOnlineState is the state of a fresh session.
func InitialOnlineState() OnlineState {
	return OnlineState{Version: 1, CompletedBoundaryRequestCounts: []int{}}
}

// Valid reports whether a decoded state is well formed.
func (s OnlineState) Valid() bool {
	if s.Version != 1 || s.Epoch < 0 || s.RequestCount < 0 || s.LastBoundaryRequestCount < 0 || s.LastBoundaryRequestCount > s.RequestCount {
		return false
	}
	if s.PositiveContextDeltaTotal < 0 || s.PositiveContextDeltaCount < 0 || s.NativeCompactionCount < 0 || s.CacheDebtTokens < 0 || s.CacheDebtRepaymentTokens < 0 {
		return false
	}
	if s.LastContextTokens != nil && *s.LastContextTokens < 0 {
		return false
	}
	for _, c := range s.CompletedBoundaryRequestCounts {
		if c < 0 {
			return false
		}
	}
	return true
}

// RecordProviderRequest counts a request and its context growth, and
// repays carried cache debt.
func (s OnlineState) RecordProviderRequest(contextTokens int) OnlineState {
	delta := 0.0
	if s.LastContextTokens != nil {
		delta = float64(contextTokens - *s.LastContextTokens)
	}
	debt := max(s.CacheDebtTokens-s.CacheDebtRepaymentTokens, 0)
	out := s
	out.RequestCount++
	tokens := contextTokens
	out.LastContextTokens = &tokens
	if delta > 0 {
		out.PositiveContextDeltaTotal += delta
		out.PositiveContextDeltaCount++
	}
	out.CacheDebtTokens = debt
	if debt == 0 {
		out.CacheDebtRepaymentTokens = 0
	}
	return out
}

// RecordBoundary records a completed work item.
func (s OnlineState) RecordBoundary() OnlineState {
	interval := max(s.RequestCount-s.LastBoundaryRequestCount, 0)
	out := s
	out.LastBoundaryRequestCount = s.RequestCount
	out.CompletedBoundaryRequestCounts = append(append([]int(nil), s.CompletedBoundaryRequestCounts...), interval)
	return out
}

// RecordCompaction starts a new epoch after any compaction, carrying the
// cache debt a boundary compaction incurred.
func (s OnlineState) RecordCompaction(debtTokens, repaymentTokens float64) OnlineState {
	out := s
	out.Epoch++
	out.LastContextTokens = nil
	out.PositiveContextDeltaTotal = 0
	out.PositiveContextDeltaCount = 0
	out.NativeCompactionCount++
	out.CacheDebtTokens = max(0, debtTokens)
	out.CacheDebtRepaymentTokens = max(0, repaymentTokens)
	return out
}

// RecordCorrection resets after the user steers or corrects the agent.
func (s OnlineState) RecordCorrection() OnlineState {
	out := s
	out.Epoch++
	out.LastBoundaryRequestCount = s.RequestCount
	out.CompletedBoundaryRequestCounts = []int{}
	out.LastContextTokens = nil
	out.PositiveContextDeltaTotal = 0
	out.PositiveContextDeltaCount = 0
	out.CacheDebtTokens = 0
	out.CacheDebtRepaymentTokens = 0
	return out
}
