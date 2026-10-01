package ai

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// ─── Model database ──────────────────────────────────────────────────────────
//
// wopr keeps no hand-written model list. A provider's models come from the
// provider itself (its model list, cached by the caller and handed over with
// SetLiveModels); what a list leaves out — prices, limits, thinking levels,
// image input — comes from models.dev (an embedded snapshot until
// SetModelsDev hands over a fresher copy); and how to talk to each provider
// — API, base URL, headers, compat flags — comes from the per-provider and
// per-family rules in modeldb_rules.go.

// KnownModel is what wopr knows about one provider model.
type KnownModel struct {
	ID                   string
	Provider             string
	DisplayName          string
	API                  API
	BaseURL              string
	Headers              map[string]string
	Compat               *ModelCompat
	ThinkingLevelMap     map[ThinkingLevel]*string
	SamplingParams       map[string]any
	PromptCache          ModelPromptCache
	InputLimits          *ModelInputLimits
	ContextWindow        int
	MaxOutputTokens      int
	InputCostPerMTokens  float64
	OutputCostPerMTokens float64
	CacheReadCost        float64
	CacheWriteCost       float64
	Tiers                []CostTier
	// PriceUnknown marks a model nothing gave a price for: its zero prices
	// mean "unknown", never "free".
	PriceUnknown bool
	Reasoning    bool
	Capabilities []string
	// Inferred marks a model neither the provider's list nor models.dev
	// names: its details are borrowed from its closest relative.
	Inferred bool
	// Released is the model's release date (YYYY-MM-DD), or "".
	Released string
}

// LiveModel is one entry of a provider's own model list, with whatever
// details the list carries. Zero values mean the list didn't say.
type LiveModel struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Context   int      `json:"context,omitempty"`
	MaxOutput int      `json:"maxOutput,omitempty"`
	Input     []string `json:"input,omitempty"`
	// Efforts are the reasoning effort values the provider lists.
	Efforts []string `json:"efforts,omitempty"`
	// Adaptive reports Anthropic's adaptive thinking.
	Adaptive *bool `json:"adaptive,omitempty"`
	// Released is the release date the list gives (YYYY-MM-DD), or "".
	Released string `json:"released,omitempty"`
	// Prices per million tokens, for lists that carry them (OpenRouter).
	InputCost      *float64 `json:"inputCost,omitempty"`
	OutputCost     *float64 `json:"outputCost,omitempty"`
	CacheReadCost  *float64 `json:"cacheReadCost,omitempty"`
	CacheWriteCost *float64 `json:"cacheWriteCost,omitempty"`
}

// models.dev's api.json, decoded to the fields wopr uses. The embedded
// snapshot is the same shape, trimmed to these fields and to the providers
// wopr has rules for (make models-snapshot).
type modelsDevProvider struct {
	ID     string                    `json:"id,omitempty"`
	API    string                    `json:"api,omitempty"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string               `json:"id"`
	Name             string               `json:"name,omitempty"`
	Family           string               `json:"family,omitempty"`
	Attachment       bool                 `json:"attachment,omitempty"`
	Reasoning        bool                 `json:"reasoning,omitempty"`
	ReasoningOptions []modelsDevReasoning `json:"reasoning_options,omitempty"`
	ToolCall         bool                 `json:"tool_call,omitempty"`
	StructuredOutput *bool                `json:"structured_output,omitempty"`
	Temperature      *bool                `json:"temperature,omitempty"`
	Modalities       modelsDevModalities  `json:"modalities"`
	Limit            modelsDevLimit       `json:"limit"`
	Cost             *modelsDevCost       `json:"cost,omitempty"`
	Provider         *modelsDevModelHost  `json:"provider,omitempty"`
	Status           string               `json:"status,omitempty"`
	ReleaseDate      string               `json:"release_date,omitempty"`
}

type modelsDevReasoning struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
}

type modelsDevModalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type modelsDevLimit struct {
	Context int `json:"context,omitempty"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output,omitempty"`
}

type modelsDevCost struct {
	Input      float64             `json:"input"`
	Output     float64             `json:"output"`
	CacheRead  float64             `json:"cache_read,omitempty"`
	CacheWrite float64             `json:"cache_write,omitempty"`
	Tiers      []modelsDevCostTier `json:"tiers,omitempty"`
}

type modelsDevCostTier struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	Tier       struct {
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"tier"`
}

// modelsDevModelHost is a model's own SDK hint inside an aggregator's list
// (opencode serves some models over the Anthropic or Responses API).
type modelsDevModelHost struct {
	NPM string `json:"npm,omitempty"`
	API string `json:"api,omitempty"`
}

// chats reports whether a models.dev entry is a chat model wopr can drive:
// it calls tools and answers in text.
func (m *modelsDevModel) chats() bool {
	return m.ToolCall && (len(m.Modalities.Output) == 0 || slices.Contains(m.Modalities.Output, "text"))
}

func (m *modelsDevModel) efforts() []string {
	for _, o := range m.ReasoningOptions {
		if o.Type == "effort" {
			return o.Values
		}
	}
	return nil
}

func (m *modelsDevModel) hasOption(kind string) bool {
	return slices.ContainsFunc(m.ReasoningOptions, func(o modelsDevReasoning) bool { return o.Type == kind })
}

//go:embed modelsdev.json
var modelsDevSnapshot []byte

// ParseModelsDev decodes models.dev's api.json (or the trimmed snapshot).
func ParseModelsDev(raw []byte) (map[string]modelsDevProvider, error) {
	var out map[string]modelsDevProvider
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("models.dev: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models.dev: no providers")
	}
	return out, nil
}

// TrimModelsDev keeps the providers wopr has rules for and the fields wopr
// reads, for the embedded snapshot and the on-disk cache.
func TrimModelsDev(raw []byte) ([]byte, error) {
	all, err := ParseModelsDev(raw)
	if err != nil {
		return nil, err
	}
	keep := map[string]modelsDevProvider{}
	for _, rule := range providerRules {
		if rule.modelsDev == "" {
			continue
		}
		p, ok := all[rule.modelsDev]
		if !ok {
			continue
		}
		models := map[string]modelsDevModel{}
		for id, m := range p.Models {
			if m.chats() {
				models[id] = m
			}
		}
		keep[rule.modelsDev] = modelsDevProvider{ID: p.ID, API: p.API, Models: models}
	}
	return json.MarshalIndent(keep, "", " ")
}

// modelDB is the composed registry. It's rebuilt whenever models.dev data
// or a provider's live list changes.
var modelDB struct {
	sync.RWMutex
	built bool
	md    map[string]modelsDevProvider
	live  map[string][]LiveModel
	// listed is what each provider offers: its live list when wopr has
	// one, else models.dev's. known adds every model wopr can resolve
	// (models.dev entries a live list leaves out, such as aliases).
	listed    []KnownModel
	known     map[string]*KnownModel
	byID      map[string][]*KnownModel
	providers []string
}

func ensureModelDB() {
	modelDB.RLock()
	built := modelDB.built
	modelDB.RUnlock()
	if built {
		return
	}
	modelDB.Lock()
	defer modelDB.Unlock()
	if !modelDB.built {
		if modelDB.md == nil {
			md, err := ParseModelsDev(modelsDevSnapshot)
			if err != nil {
				panic("ai: embedded models.dev snapshot: " + err.Error())
			}
			modelDB.md = md
		}
		rebuildModelDB()
	}
}

// SetModelsDev replaces the models.dev data (a fresher copy than the
// embedded snapshot) and rebuilds the registry.
func SetModelsDev(raw []byte) error {
	md, err := ParseModelsDev(raw)
	if err != nil {
		return err
	}
	modelDB.Lock()
	defer modelDB.Unlock()
	modelDB.md = md
	rebuildModelDB()
	return nil
}

// SetLiveModels records a provider's own model list and rebuilds the
// registry; nil forgets it.
func SetLiveModels(provider string, models []LiveModel) {
	ensureModelDB()
	modelDB.Lock()
	defer modelDB.Unlock()
	if modelDB.live == nil {
		modelDB.live = map[string][]LiveModel{}
	}
	if models == nil {
		delete(modelDB.live, provider)
	} else {
		modelDB.live[provider] = slices.Clone(models)
	}
	rebuildModelDB()
}

// SetAllLiveModels records several providers' lists with one rebuild.
func SetAllLiveModels(lists map[string][]LiveModel) {
	if len(lists) == 0 {
		return
	}
	ensureModelDB()
	modelDB.Lock()
	defer modelDB.Unlock()
	if modelDB.live == nil {
		modelDB.live = map[string][]LiveModel{}
	}
	for provider, models := range lists {
		modelDB.live[provider] = slices.Clone(models)
	}
	rebuildModelDB()
}

// LiveModels returns the provider list SetLiveModels last recorded.
func LiveModels(provider string) ([]LiveModel, bool) {
	ensureModelDB()
	modelDB.RLock()
	defer modelDB.RUnlock()
	models, ok := modelDB.live[provider]
	return slices.Clone(models), ok
}

// HasModelRules reports whether wopr knows how to talk to a provider.
func HasModelRules(provider string) bool {
	_, ok := ruleFor(provider)
	return ok
}

// ProviderEndpoint is the API and base URL of a provider's model list; the
// base is empty when it depends on the user's account (a templated URL).
func ProviderEndpoint(provider string) (API, string) {
	rule, ok := ruleFor(provider)
	if !ok {
		return "", ""
	}
	api, base := rule.api, strings.TrimRight(rule.baseURL, "/")
	if rule.listBase != "" {
		api, base = rule.listAPI, rule.listBase
	}
	if strings.Contains(base, "{") {
		base = ""
	}
	return api, base
}

// rebuildModelDB composes the registry; callers hold the write lock.
func rebuildModelDB() {
	var listed []KnownModel
	extra := map[string]KnownModel{}
	for i := range providerRules {
		rule := &providerRules[i]
		fromMD := rule.fromModelsDev(modelDB.md)
		live, hasLive := modelDB.live[rule.id]
		if !hasLive {
			listed = append(listed, fromMD...)
			continue
		}
		byID := make(map[string]KnownModel, len(fromMD))
		for _, m := range fromMD {
			byID[m.ID] = m
		}
		listedHere := map[string]bool{}
		for _, lm := range live {
			if lm.ID == "" || listedHere[lm.ID] {
				continue
			}
			listedHere[lm.ID] = true
			listed = append(listed, rule.fromLive(lm, byID, fromMD))
		}
		for _, m := range fromMD {
			if !listedHere[m.ID] {
				extra[rule.id+"/"+m.ID] = m
			}
		}
	}
	known := make(map[string]*KnownModel, len(listed)+len(extra))
	byID := make(map[string][]*KnownModel, len(listed))
	seenProvider := map[string]bool{}
	var providers []string
	for i := range listed {
		m := &listed[i]
		known[m.Provider+"/"+m.ID] = m
		byID[m.ID] = append(byID[m.ID], m)
		if !seenProvider[m.Provider] {
			seenProvider[m.Provider] = true
			providers = append(providers, m.Provider)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(extra)) {
		m := extra[key]
		known[key] = &m
	}
	slices.Sort(providers)
	modelDB.listed, modelDB.known, modelDB.byID, modelDB.providers = listed, known, byID, providers
	modelDB.built = true
}

// fromModelsDev builds the provider's models from models.dev, sorted by id.
func (r *providerRule) fromModelsDev(md map[string]modelsDevProvider) []KnownModel {
	if r.modelsDev == "" {
		return nil
	}
	p, ok := md[r.modelsDev]
	if !ok {
		return nil
	}
	var out []KnownModel
	for _, mdID := range slices.Sorted(maps.Keys(p.Models)) {
		m := p.Models[mdID]
		if !m.chats() {
			continue
		}
		id := mdID
		if r.mapID != nil {
			if id = r.mapID(mdID); id == "" {
				continue
			}
		}
		if r.include != nil && !r.include(id) {
			continue
		}
		out = append(out, r.build(id, &m, nil))
	}
	return out
}

// fromLive builds one live-listed model: its models.dev entry with the
// list's details on top, or, for a model models.dev doesn't know yet, its
// closest relative's.
func (r *providerRule) fromLive(lm LiveModel, byID map[string]KnownModel, fromMD []KnownModel) KnownModel {
	md := r.modelsDevEntry(lm.ID)
	if md == nil {
		if rel, ok := closestRelative(lm.ID, fromMD); ok {
			md = r.modelsDevEntry(rel.ID)
		}
		m := r.build(lm.ID, md, &lm)
		m.Inferred = md == nil || md.ID != lm.ID
		if md != nil && md.ID != lm.ID {
			// A relative's price is a guess; say it's unknown.
			m.InputCostPerMTokens, m.OutputCostPerMTokens, m.CacheReadCost, m.CacheWriteCost = 0, 0, 0, 0
			m.Tiers = nil
			m.PriceUnknown = !hasLivePrice(lm)
			applyLivePrice(&m, lm)
			if lm.Name == "" {
				m.DisplayName = lm.ID
			}
		}
		return m
	}
	return r.build(lm.ID, md, &lm)
}

// modelsDevEntry finds the models.dev entry behind a provider model id.
func (r *providerRule) modelsDevEntry(id string) *modelsDevModel {
	p := modelDB.md[r.modelsDev]
	if r.modelsDev == "" || p.Models == nil {
		return nil
	}
	if m, ok := p.Models[id]; ok {
		return &m
	}
	if r.mapID != nil {
		for mdID, m := range p.Models {
			if r.mapID(mdID) == id {
				return &m
			}
		}
	}
	return nil
}

// closestRelative is the provider model whose id shares the longest
// leading run with id (claude-sonnet-5-5 → claude-sonnet-5), preferring the
// newest-looking (highest id) among equals.
func closestRelative(id string, models []KnownModel) (KnownModel, bool) {
	best, bestLen := KnownModel{}, 0
	norm := strings.ToLower(id)
	for _, m := range models {
		n := commonPrefix(norm, strings.ToLower(m.ID))
		if n > bestLen || n == bestLen && n > 0 && m.ID > best.ID {
			best, bestLen = m, n
		}
	}
	// A relative must share more than a vendor prefix ("gpt-", "claude-").
	if bestLen < 6 {
		return KnownModel{}, false
	}
	return best, true
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func hasLivePrice(lm LiveModel) bool { return lm.InputCost != nil && lm.OutputCost != nil }

func applyLivePrice(m *KnownModel, lm LiveModel) {
	if !hasLivePrice(lm) {
		return
	}
	m.InputCostPerMTokens, m.OutputCostPerMTokens = *lm.InputCost, *lm.OutputCost
	if lm.CacheReadCost != nil {
		m.CacheReadCost = *lm.CacheReadCost
	}
	if lm.CacheWriteCost != nil {
		m.CacheWriteCost = *lm.CacheWriteCost
	}
	m.PriceUnknown = false
}

// inferModel builds a model the provider hasn't listed and models.dev
// doesn't know, from its closest relative, for a provider whose own list
// wopr hasn't read yet. Callers hold at least the read lock.
func inferModel(provider, id string) (*KnownModel, bool) {
	rule, ok := ruleFor(provider)
	if !ok || id == "" {
		return nil, false
	}
	if _, hasLive := modelDB.live[provider]; hasLive {
		return nil, false // the provider's list is the truth
	}
	m := rule.fromLive(LiveModel{ID: id}, nil, rule.fromModelsDev(modelDB.md))
	return &m, true
}

func ruleFor(provider string) (*providerRule, bool) {
	for i := range providerRules {
		if providerRules[i].id == provider {
			return &providerRules[i], true
		}
	}
	return nil, false
}
