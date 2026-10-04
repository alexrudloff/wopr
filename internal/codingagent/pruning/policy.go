package pruning

import (
	"slices"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
)

// CacheTTL is how long a provider keeps a prompt prefix cached after its last
// use (Anthropic's default, and the low end of OpenAI's automatic caching).
const CacheTTL = 5 * time.Minute

// LongCacheTTL is the long prompt cache's lifetime (Anthropic's 1 hour; the
// low end of the long caches).
const LongCacheTTL = time.Hour

// LongCacheWriteReadRatio is a long-cache write's cost in cache reads
// (Anthropic: 2x input to write, 0.1x to read).
const LongCacheWriteReadRatio = 20

// MinBatchTokens is the smallest saving that justifies breaking a warm
// cache on its own.
const MinBatchTokens = 2000

// Policy describes the cache state when a batch is considered.
type Policy struct {
	// Cold means the provider's prefix cache is already lost (idle past the
	// TTL, just compacted, first request of a process), so every edit is free.
	Cold bool
	// Force applies every edit regardless of cost, as before a compaction
	// the edits might avoid.
	Force bool
	// Horizon is the expected number of further requests that re-send the
	// context.
	Horizon int
	// WriteReadRatio is the cost of a cache write in cache reads.
	WriteReadRatio float64
}

// Horizon estimates the requests left in a run from the requests already in
// it (a session that has run long tends to run on), bounded to [4, 32].
func Horizon(requests int) int {
	return min(max(requests, 4), 32)
}

// Choose selects which edits to apply now. mandatory edits (a compress the
// model asked for) always apply, and they break the cache from their first
// index, so automatic edits at or after that index ride along for free. An
// earlier automatic edit applies only when the saving over the horizon pays
// for rewriting the cache from its position:
//
//	saved * horizon >= (suffix tokens - saved) * (writeReadRatio - 1)
func Choose(auto, mandatory []Edit, items []Item, p Policy) []Edit {
	if p.Cold || p.Force {
		return slices.Concat(mandatory, auto)
	}
	suffix := make([]int, len(items)+1)
	for i := len(items) - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + items[i].Tokens
	}
	breakAt := len(items)
	for _, e := range mandatory {
		breakAt = min(breakAt, e.Index)
	}
	sorted := slices.Clone(auto)
	slices.SortFunc(sorted, func(a, b Edit) int { return a.Index - b.Index })
	free := 0
	for free < len(sorted) && sorted[free].Index < breakAt {
		free++
	}
	// free is the first auto edit at or after the break. Extend the batch to
	// the earliest edit whose saving pays for the extra rewrite.
	ratio := p.WriteReadRatio
	if ratio <= 1 {
		ratio = efficiency.DefaultCacheWriteReadRatio
	}
	start := free
	for k := 0; k < free; k++ {
		saved := 0
		for _, e := range sorted[k:free] {
			saved += e.Saved
		}
		if len(mandatory) == 0 && saved < MinBatchTokens {
			continue
		}
		rewritten := suffix[sorted[k].Index] - suffix[breakAt] - saved
		if float64(saved*max(p.Horizon, 1)) >= float64(max(rewritten, 0))*(ratio-1) {
			start = k
			break
		}
	}
	if len(mandatory) == 0 && start == free {
		return nil // nothing pays, and no break is coming
	}
	return slices.Concat(mandatory, sorted[start:])
}
