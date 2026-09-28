package ai

import (
	"testing"
)

func TestLookupByFullyQualified(t *testing.T) {
	m, ok := LookupModel("openai/gpt-4o")
	if !ok {
		t.Fatal("openai/gpt-4o not found")
	}
	if m.Provider != "openai" {
		t.Fatalf("provider = %q want openai", m.Provider)
	}
	if m.ContextWindow == 0 {
		t.Errorf("gpt-4o context window = 0 (parser regression)")
	}
}

// LookupModelExact resolves only the fully-qualified provider/id key and must
// NOT borrow another provider's same-named model the way LookupModel does.
// This is the property model resolution relies on to avoid giving
// github-copilot/gpt-4o openai gpt-4o's capabilities.
func TestLookupModelExact_NoCrossProviderBorrow(t *testing.T) {
	// LookupModel cross-resolves the bare id (openai's gpt-4o)...
	if _, ok := LookupModel("github-copilot/gpt-4o"); !ok {
		t.Fatal("precondition: LookupModel should cross-resolve github-copilot/gpt-4o to openai's")
	}
	// ...but LookupModelExact must not, since copilot has no gpt-4o.
	if _, ok := LookupModelExact("github-copilot/gpt-4o"); ok {
		t.Error("LookupModelExact resolved github-copilot/gpt-4o; copilot has no gpt-4o")
	}
	// A genuine provider/model still resolves.
	if _, ok := LookupModelExact("github-copilot/gpt-5.4"); !ok {
		t.Error("LookupModelExact should resolve github-copilot/gpt-5.4")
	}
	if _, ok := LookupModelExact(""); ok {
		t.Error("empty spec must miss")
	}
}
