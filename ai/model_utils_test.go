package ai

import (
	"testing"
)

// TestCalculateCost verifies the cost model: separate cache-read/write
// rates, 1h cache writes billed at 2x the base input rate, and request-wide
// tier selection by total input tokens. CalculateCost fills usage.Cost.
func TestCalculateCost(t *testing.T) {
	intPtr := func(value int) *int { return &value }
	base := &Model{Capabilities: ModelCapabilities{
		InputCostPer1M: 3, OutputCostPer1M: 15,
		CacheReadCostPer1M: 0.3, CacheWriteCostPer1M: 3.75,
	}}
	tiered := &Model{Capabilities: ModelCapabilities{
		InputCostPer1M: 3, OutputCostPer1M: 15,
		CacheReadCostPer1M: 0.3, CacheWriteCostPer1M: 3.75,
		CostTiers: []CostTier{
			{InputTokensAbove: 100_000, InputCostPer1M: 4, OutputCostPer1M: 20, CacheReadCostPer1M: 0.4, CacheWriteCostPer1M: 5},
			{InputTokensAbove: 500_000, InputCostPer1M: 6, OutputCostPer1M: 30, CacheReadCostPer1M: 0.6, CacheWriteCostPer1M: 7.5},
		},
	}}
	const eps = 1e-9
	cases := []struct {
		name string
		m    *Model
		u    *Usage
		want float64
	}{
		{"nil model", nil, &Usage{Input: 1_000_000}, 0},
		{"input+output base rates", base, &Usage{Input: 1_000_000, Output: 1_000_000}, 18},
		{"separate cache rates", base, &Usage{CacheRead: 1_000_000, CacheWrite: 1_000_000}, 4.05},
		{"1h write at 2x input rate", base, &Usage{CacheWrite: 1_000_000, CacheWrite1h: intPtr(1_000_000)}, 6.0},
		{"mixed short/long write", base, &Usage{CacheWrite: 1_000_000, CacheWrite1h: intPtr(400_000)}, 4.65},
		{"below tier uses base", tiered, &Usage{Input: 50_000}, 0.15},
		{"above tier uses tier rate", tiered, &Usage{Input: 300_000}, 1.2},
		{"highest matching tier wins", tiered, &Usage{Input: 600_000}, 3.6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CalculateCost(tc.m, tc.u)
			if got.Total < tc.want-eps || got.Total > tc.want+eps {
				t.Fatalf("CalculateCost().Total = %v, want %v", got.Total, tc.want)
			}
			if tc.u.Cost != got {
				t.Fatalf("usage.Cost = %#v, want the returned %#v", tc.u.Cost, got)
			}
		})
	}
	if got := CalculateCost(base, nil); got != (UsageCost{}) {
		t.Fatalf("CalculateCost(nil usage) = %#v", got)
	}
}

// TestCalculateCostTierBoundary pins cost values for
// openai/gpt-5.5: a prompt exactly at the tier threshold keeps the
// base rates; one token more switches every rate to the tier.
func TestCalculateCostTierBoundary(t *testing.T) {
	generated, ok := LookupModelExact("openai/gpt-5.5")
	if !ok {
		t.Fatal("catalog model missing")
	}
	model := &Model{Capabilities: generated.ToCapabilities()}
	for _, tc := range []struct {
		input int
		want  UsageCost
	}{
		{272_000, UsageCost{Input: 1.36, Output: 0.030000000000000002, Total: 1.3900000000000001}},
		{272_001, UsageCost{Input: 2.7200100000000003, Output: 0.045000000000000005, Total: 2.76501}},
	} {
		usage := &Usage{Input: tc.input, Output: 1000}
		if got := CalculateCost(model, usage); got != tc.want {
			t.Fatalf("input %d: cost = %#v, want %#v", tc.input, got, tc.want)
		}
	}
}
