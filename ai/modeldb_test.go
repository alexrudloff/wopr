package ai

import "testing"

// A model nothing prices must not look free: a provider-listed model
// models.dev doesn't know yet borrows its relative's limits but not its
// price, and usage priced with it is marked unknown.
func TestUnknownPriceIsNotFree(t *testing.T) {
	SetLiveModels("anthropic", []LiveModel{{ID: "claude-sonnet-9"}, {ID: "claude-sonnet-5-5"}})
	t.Cleanup(func() { SetLiveModels("anthropic", nil) })

	unknown, ok := LookupModelExact("anthropic/claude-sonnet-9")
	if !ok || !unknown.Inferred || !unknown.PriceUnknown || unknown.InputCostPerMTokens != 0 {
		t.Fatalf("claude-sonnet-9 = %+v, want an inferred model with an unknown price", unknown)
	}
	usage := &Usage{Input: 1000, Output: 100}
	if cost := CalculateCost(&Model{Capabilities: unknown.ToCapabilities()}, usage); !cost.Unknown {
		t.Fatalf("usage cost = %+v, want Unknown", cost)
	}

	priced, ok := LookupModelExact("anthropic/claude-sonnet-5-5")
	if !ok || priced.PriceUnknown || priced.InputCostPerMTokens == 0 {
		t.Fatalf("claude-sonnet-5-5 = %+v, want models.dev's price", priced)
	}
	usage = &Usage{Input: 1000, Output: 100}
	if cost := CalculateCost(&Model{Capabilities: priced.ToCapabilities()}, usage); cost.Unknown || cost.Total == 0 {
		t.Fatalf("usage cost = %+v, want a known, non-zero cost", cost)
	}
}
