package ai

import (
	"maps"
	"slices"
	"strings"
)

// ─── Model Registry ───────────────────────────────────────────────────────────
//
// Lookups over the model database (modeldb.go): by full id
// (`provider/model`) or bare model id. Status line, /cost, and `--diagnose`
// all consume from here.

// LookupModel resolves a spec like "<provider>/<model-id>" or just
// "<model-id>". Returns the matching model and true on success.
//
// When only a bare model id is given and multiple providers ship a
// model with the same id, the first match in alphabetical-provider order
// wins. Callers wanting deterministic resolution should pass the full
// "<provider>/<model-id>" form.
func LookupModel(spec string) (*KnownModel, bool) {
	if spec == "" {
		return nil, false
	}
	if m, ok := LookupModelExact(spec); ok {
		return m, true
	}
	ensureModelDB()
	modelDB.RLock()
	defer modelDB.RUnlock()
	id := spec
	if _, after, ok := strings.Cut(spec, "/"); ok {
		id = after
	}
	if entries := modelDB.byID[id]; len(entries) > 0 {
		m := *entries[0]
		return &m, true
	}
	return nil, false
}

// LookupModelExact resolves only the fully-qualified "provider/id" key,
// without LookupModel's bare-id fallback across other providers. This is
// what model resolution needs so that "github-copilot/gpt-4o" yields no
// match when copilot lacks gpt-4o, instead of silently borrowing openai's
// gpt-4o capabilities.
func LookupModelExact(spec string) (*KnownModel, bool) {
	if spec == "" {
		return nil, false
	}
	ensureModelDB()
	modelDB.RLock()
	defer modelDB.RUnlock()
	m, ok := modelDB.known[spec]
	if !ok {
		return nil, false
	}
	out := *m
	return &out, true
}

// InferModel builds a model of a known provider that neither the
// provider's list nor models.dev names, from its closest relative under the
// same provider (claude-sonnet-6 from claude-sonnet-5-5). It refuses once
// wopr has the provider's own list and the list leaves the model out.
func InferModel(spec string) (*KnownModel, bool) {
	provider, id, ok := strings.Cut(spec, "/")
	if !ok {
		return nil, false
	}
	if m, ok := LookupModelExact(spec); ok {
		return m, true
	}
	modelDB.RLock()
	defer modelDB.RUnlock()
	return inferModel(provider, id)
}

// ToCapabilities lifts a KnownModel into the runtime
// ModelCapabilities consumed by status line and provider routing.
func (m *KnownModel) ToCapabilities() ModelCapabilities {
	caps := ModelCapabilities{
		ContextWindow:       m.ContextWindow,
		MaxOutputTokens:     m.MaxOutputTokens,
		InputCostPer1M:      m.InputCostPerMTokens,
		OutputCostPer1M:     m.OutputCostPerMTokens,
		CacheReadCostPer1M:  m.CacheReadCost,
		CacheWriteCostPer1M: m.CacheWriteCost,
		CostTiers:           m.Tiers,
		PriceUnknown:        m.PriceUnknown,
		SupportsToolUse:     true, // the catalog assumes tool-use universally; per-API gating happens at provider layer
	}
	for _, c := range m.Capabilities {
		if c == "image" {
			caps.SupportsImages = true
		}
	}
	for _, level := range GetSupportedThinkingLevels(&Model{
		Capabilities:     ModelCapabilities{MaxThinking: thinkingMaxLevel(m.Reasoning, m.ThinkingLevelMap)},
		ThinkingLevelMap: cloneThinkingLevelMap(m.ThinkingLevelMap),
	}) {
		if CompareThinkingLevels(level, caps.MaxThinking) > 0 {
			caps.MaxThinking = level
		}
	}
	return caps
}

func thinkingMaxLevel(reasoning bool, levelMap ThinkingLevelMap) ThinkingLevel {
	if !reasoning {
		return ""
	}
	maxLevel := ThinkingHigh
	for level, mapped := range levelMap {
		if mapped == nil {
			continue
		}
		if CompareThinkingLevels(level, maxLevel) > 0 {
			maxLevel = level
		}
	}
	return maxLevel
}

// ToModel lifts a generated catalog entry into the runtime model metadata shape
// used by provider-specific compat helpers and tests.
func (m *KnownModel) ToModel() *Model {
	if m == nil {
		return nil
	}
	return &Model{
		ID:          m.ID,
		DisplayName: m.DisplayName,
		Capabilities: ModelCapabilities{
			MaxThinking:     thinkingMaxLevel(m.Reasoning, m.ThinkingLevelMap),
			MaxOutputTokens: m.MaxOutputTokens,
		},
		Input:            append([]string(nil), m.Capabilities...),
		ThinkingLevelMap: cloneThinkingLevelMap(m.ThinkingLevelMap),
		SamplingParams:   maps.Clone(m.SamplingParams),
		PromptCache:      maps.Clone(m.PromptCache),
		InputLimits:      m.InputLimits.Clone(),
		ProviderMeta: ProviderMetadata{
			ProviderID: m.Provider,
			API:        m.API,
			BaseURL:    m.BaseURL,
			Headers:    cloneStringMap(m.Headers),
			Compat:     cloneCompat(m.Compat),
			Reasoning:  m.Reasoning,
		},
	}
}

// ListModels returns the models a provider offers (every provider's when
// provider is empty): its own list when wopr has read it, else models.dev's.
func ListModels(provider string) []KnownModel {
	ensureModelDB()
	modelDB.RLock()
	defer modelDB.RUnlock()
	out := make([]KnownModel, 0, len(modelDB.listed))
	for _, m := range modelDB.listed {
		if provider == "" || m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}

// ListProviders returns the sorted providers that offer models.
func ListProviders() []string {
	ensureModelDB()
	modelDB.RLock()
	defer modelDB.RUnlock()
	return slices.Clone(modelDB.providers)
}
