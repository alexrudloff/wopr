package codingagent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/text"
)

// setupKind is how a model was added.
type setupKind int

const (
	setupViaSubscription setupKind = iota
	setupViaAPIKey
	setupViaEndpoint
)

// setupModel is one model setup adds, with what it measured and asked.
type setupModel struct {
	Kind            setupKind
	Provider, Model string
	Name            string
	// Endpoint is set for endpoint models.
	Endpoint *setupEndpoint
	// Context and MaxOutput are the model's window and output cap; zero is
	// unknown.
	Context, MaxOutput int
	// Tier is the cost class; Capability the router's 0..1 strength.
	Tier       string
	Capability float64
	Uncensored bool
	// PrivacySafe marks a model whose endpoint keeps data with the user.
	PrivacySafe bool
	// Measure is what the probes found; nil when not measured.
	Measure *modelMeasure

	// Existing marks a model configured before this run. Ref is its router
	// entry and FromTier the tier holding it (nil and "" when not routed);
	// Defined reports models.json defines it. Edited and Removed are the
	// user's changes to it.
	Existing        bool
	Ref             *router.ModelRef
	FromTier        string
	Defined         bool
	Edited, Removed bool
	// NoRouting keeps the model out of routing; /model can still pick it.
	NoRouting bool
	// Strengths are the brief domains (router.Domains) the model is good
	// at; routing prefers it for those among equally suitable models.
	Strengths []string
	// origName and origContext are the saved values, so an edit writes only
	// what changed.
	origName    string
	origContext int
}

// changed reports an existing model the user edited or removed.
func (s *setupModel) changed() bool { return s.Existing && (s.Edited || s.Removed) }

func (s *setupModel) spec() string { return s.Provider + "/" + s.Model }

// routable reports whether the router may send work to the model: a model
// that measurably cannot call tools can't do an agent's work.
func (s *setupModel) routable() bool {
	return s.Measure == nil || s.Measure.Tools == nil || *s.Measure.Tools
}

// ─── models.json ─────────────────────────────────────────────────────────

// modelsJSONProvider is a provider entry setup writes, in key order.
type modelsJSONProvider struct {
	Name       string            `json:"name,omitempty"`
	BaseURL    string            `json:"baseUrl"`
	API        string            `json:"api"`
	AuthHeader bool              `json:"authHeader"`
	Compat     *setupCompat      `json:"compat,omitempty"`
	Models     []modelsJSONModel `json:"models"`
}

type modelsJSONModel struct {
	ID               string              `json:"id"`
	Name             string              `json:"name,omitempty"`
	Reasoning        bool                `json:"reasoning"`
	ThinkingLevelMap ai.ThinkingLevelMap `json:"thinkingLevelMap,omitempty"`
	ContextWindow    int                 `json:"contextWindow,omitempty"`
	MaxTokens        int                 `json:"maxTokens,omitempty"`
	Compat           *setupCompat        `json:"compat,omitempty"`
	Cost             *modelCostJSON      `json:"cost,omitempty"`
}

type setupCompat struct {
	ThinkingFormat           string `json:"thinkingFormat,omitempty"`
	SupportsReasoningEffort  *bool  `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming *bool  `json:"supportsUsageInStreaming,omitempty"`
}

// compatFor is the compat block a measurement calls for, or nil. A server
// that validates reasoning_effort gets the "baseten" format, which sends
// the field for every level including off.
func compatFor(m *modelMeasure) *setupCompat {
	if m == nil {
		return nil
	}
	var c setupCompat
	if m.Effort {
		c.ThinkingFormat, c.SupportsReasoningEffort = "baseten", new(true)
	}
	if m.UsageRefused {
		c.SupportsUsageInStreaming = new(false)
	}
	if c == (setupCompat{}) {
		return nil
	}
	return &c
}

// providerSetupCompat reads the fields setup writes from a provider's own
// compat block.
func providerSetupCompat(prov providerConfig) *setupCompat {
	if prov.Compat == nil {
		return nil
	}
	data, err := json.Marshal(prov.Compat)
	if err != nil {
		return nil
	}
	var c setupCompat
	if json.Unmarshal(data, &c) != nil || c == (setupCompat{}) {
		return nil
	}
	return &c
}

func sameCompat(a, b *setupCompat) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ThinkingFormat == b.ThinkingFormat && ptrEqual(a.SupportsReasoningEffort, b.SupportsReasoningEffort) &&
		ptrEqual(a.SupportsUsageInStreaming, b.SupportsUsageInStreaming)
}

func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// modelEntry is the models.json definition of an endpoint model.
func (s *setupModel) modelEntry() modelsJSONModel {
	entry := modelsJSONModel{ID: s.Model, ContextWindow: s.Context}
	if s.Name != "" && s.Name != s.Model {
		entry.Name = s.Name
	}
	if m := s.Measure; m != nil {
		entry.Reasoning = m.Effort || m.Reasoning
		entry.ThinkingLevelMap = m.LevelMap
	}
	if s.MaxOutput > 0 {
		entry.MaxTokens = s.MaxOutput
	} else if s.Context > 0 {
		entry.MaxTokens = min(16384, s.Context/4)
	}
	if s.Tier == router.CostFreeLocal || s.Tier == router.CostFreeRemote {
		entry.Cost = &modelCostJSON{}
	}
	return entry
}

// mergeModelsJSON adds the endpoint models to src, the models.json text
// ("" when there is none). A new provider becomes a new entry; models of a
// provider that already exists are appended to its list unless it has them.
// Nothing already in the file is changed.
func mergeModelsJSON(src string, models []*setupModel, existingModels ...*setupModel) (string, error) {
	var existing modelsConfig
	if strings.TrimSpace(src) != "" {
		if err := json.Unmarshal([]byte(stripJSONComments(text.StripBom(src))), &existing); err != nil {
			return "", fmt.Errorf("models.json: %w", err)
		}
	} else {
		src = "{}\n"
	}
	// Group by provider, keeping the order models were added.
	var order []string
	byProvider := map[string][]*setupModel{}
	for _, s := range models {
		if s.Endpoint == nil {
			continue
		}
		if _, seen := byProvider[s.Provider]; !seen {
			order = append(order, s.Provider)
		}
		byProvider[s.Provider] = append(byProvider[s.Provider], s)
	}
	for _, id := range order {
		group := byProvider[id]
		if prov, ok := existing.Providers[id]; ok {
			for _, s := range group {
				if providerDefinesModel(prov, s.Model) {
					continue
				}
				entry := s.modelEntry()
				if compat := compatFor(s.Measure); !sameCompat(compat, providerSetupCompat(prov)) {
					entry.Compat = compat
				}
				data, err := compactJSON(entry)
				if err != nil {
					return "", err
				}
				if src, err = jsoncAppendAt(src, data, "models", "providers", id); err != nil {
					return "", err
				}
				prov.Models = append(prov.Models, modelDefinition{ID: s.Model})
			}
			continue
		}
		ep := group[0].Endpoint
		displayName := ep.Name
		if displayName == id {
			displayName = ""
		}
		provider := modelsJSONProvider{Name: displayName, BaseURL: ep.BaseURL, API: string(ai.APIOpenAICompletions), AuthHeader: ep.APIKey != ""}
		shared := compatFor(group[0].Measure)
		for _, s := range group[1:] {
			if !sameCompat(shared, compatFor(s.Measure)) {
				shared = nil
				break
			}
		}
		provider.Compat = shared
		for _, s := range group {
			entry := s.modelEntry()
			if shared == nil {
				entry.Compat = compatFor(s.Measure)
			}
			provider.Models = append(provider.Models, entry)
		}
		data, err := compactJSON(provider)
		if err != nil {
			return "", err
		}
		if src, err = jsoncAppendAt(src, jsoncMember(id, data), "", "providers"); err != nil {
			return "", err
		}
	}
	var err error
	if src, err = applyModelsJSONChanges(src, existingModels); err != nil {
		return "", err
	}
	// The result must still parse the way the registry reads it.
	var check modelsConfig
	if err := json.Unmarshal([]byte(stripJSONComments(text.StripBom(src))), &check); err != nil {
		return "", fmt.Errorf("models.json merge produced invalid JSON: %w", err)
	}
	return src, nil
}

// definitionIndex is model's index in provider's models.json list, or -1.
func definitionIndex(src, provider, model string) (int, error) {
	var cfg modelsConfig
	if err := json.Unmarshal([]byte(stripJSONComments(text.StripBom(src))), &cfg); err != nil {
		return -1, err
	}
	return slices.IndexFunc(cfg.Providers[provider].Models, func(d modelDefinition) bool { return d.ID == model }), nil
}

// jsoncSet sets the value at path, adding the member (and any missing
// parents) when it isn't there.
func jsoncSet(src, value string, path ...string) (string, error) {
	span, ok, err := jsoncFind(src, path...)
	if err != nil {
		return "", err
	}
	if ok {
		return jsoncReplace(src, span, value), nil
	}
	last := path[len(path)-1]
	return jsoncAppendAt(src, jsoncMember(last, value), "", path[:len(path)-1]...)
}

// removeModelsJSONProvider deletes a provider from models.json.
func removeModelsJSONProvider(src, id string) (string, error) {
	span, ok, err := jsoncFind(src, "providers", id)
	if err != nil || !ok {
		return src, err
	}
	return jsoncRemove(src, span), nil
}

// applyModelsJSONChanges applies edits and removals to configured models:
// a new display name or context window goes into the model's definition
// when models.json defines it, else into the provider's modelOverrides; a
// removed definition is deleted, with its provider when it was the last
// one.
func applyModelsJSONChanges(src string, existing []*setupModel) (string, error) {
	index := func(src, provider, model string) (int, int, error) {
		var cfg modelsConfig
		if err := json.Unmarshal([]byte(stripJSONComments(text.StripBom(src))), &cfg); err != nil {
			return 0, 0, err
		}
		models := cfg.Providers[provider].Models
		return slices.IndexFunc(models, func(d modelDefinition) bool { return d.ID == model }), len(models), nil
	}
	for _, s := range existing {
		if !s.changed() {
			continue
		}
		if !s.Defined {
			if s.Removed {
				continue
			}
			override := []string{"providers", s.Provider, "modelOverrides", s.Model}
			var err error
			if s.Name != s.origName && s.Name != "" {
				if src, err = jsoncSet(src, strconv.Quote(s.Name), append(override, "name")...); err != nil {
					return "", err
				}
			}
			if s.Context != s.origContext && s.Context > 0 {
				if src, err = jsoncSet(src, strconv.Itoa(s.Context), append(override, "contextWindow")...); err != nil {
					return "", err
				}
			}
			continue
		}
		i, n, err := index(src, s.Provider, s.Model)
		if err != nil || i < 0 {
			continue
		}
		entry := []string{"providers", s.Provider, "models", "#" + strconv.Itoa(i)}
		if s.Removed {
			path := entry
			if n == 1 {
				path = []string{"providers", s.Provider}
			}
			span, ok, err := jsoncFind(src, path...)
			if err != nil {
				return "", err
			}
			if ok {
				src = jsoncRemove(src, span)
			}
			continue
		}
		if s.Name != s.origName && s.Name != "" {
			if src, err = jsoncSet(src, strconv.Quote(s.Name), append(slices.Clone(entry), "name")...); err != nil {
				return "", err
			}
		}
		if s.Context != s.origContext && s.Context > 0 {
			if src, err = jsoncSet(src, strconv.Itoa(s.Context), append(slices.Clone(entry), "contextWindow")...); err != nil {
				return "", err
			}
		}
	}
	return src, nil
}

// addEndpointProvider adds an endpoint to models.json with no models yet;
// its models are added from its menu.
func addEndpointProvider(src string, ep *setupEndpoint) (string, error) {
	if strings.TrimSpace(src) == "" {
		src = "{}\n"
	}
	name := ep.Name
	if name == ep.ID {
		name = ""
	}
	data, err := compactJSON(modelsJSONProvider{Name: name, BaseURL: ep.BaseURL, API: string(ai.APIOpenAICompletions), AuthHeader: ep.APIKey != "", Models: []modelsJSONModel{}})
	if err != nil {
		return "", err
	}
	return jsoncAppendAt(src, jsoncMember(ep.ID, data), "", "providers")
}

// jsoncAppendAt appends item to the container at path; when the path's
// last key is missing, it is created in its parent holding just item (key
// names the list or object: "models" makes an array, "" an object keyed by
// item's own key).
func jsoncAppendAt(src, item, key string, path ...string) (string, error) {
	full := path
	if key != "" {
		full = append(slices.Clone(path), key)
	}
	span, ok, err := jsoncFind(src, full...)
	if err != nil {
		return "", err
	}
	if ok {
		return jsoncAppend(src, span, item), nil
	}
	// Create the missing containers from the deepest existing parent down.
	for i, f := range slices.Backward(full) {
		parent, found, err := jsoncFind(src, full[:i]...)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		value := item
		for j := len(full) - 1; j > i; j-- {
			value = jsoncMember(full[j], wrapContainer(full[j], value))
		}
		return jsoncAppend(src, parent, jsoncMember(f, wrapContainer(f, value))), nil
	}
	return "", errors.New("no root object")
}

// wrapContainer puts item in an array for list keys and an object
// otherwise.
func wrapContainer(key, item string) string {
	if key == "models" || key == "tiers" {
		return "[\n  " + reindent(item, "  ") + "\n]"
	}
	return "{\n  " + reindent(item, "  ") + "\n}"
}

// ─── router.json ─────────────────────────────────────────────────────────

// routerPlan is what setup changes in router.json.
type routerPlan struct {
	Models []*setupModel
	// Jev replaces the classifier settings when set.
	Jev *router.JevConfig
	// Existing are the models configured before setup; the edited ones
	// change their entries and the removed ones leave routing.
	Existing []*setupModel
	// Enabled turns model routing on or off when set.
	Enabled *bool
	// Engine sets what routes (router.EngineBasic or router.EngineJev)
	// when set.
	Engine string
}

// tierFor picks the tier a model joins: an endpoint gets its own tier,
// named for its provider and probed at its model list, so one server going
// down never takes another's models with it; other models join the first
// tier of their cost class that is not an endpoint's.
func tierFor(tiers []router.TierConfig, s *setupModel) (index int, fresh router.TierConfig) {
	if s.Endpoint != nil {
		name := s.Provider
		for i, tier := range tiers {
			if tier.Name == name && tier.Cost == s.Tier {
				return i, router.TierConfig{}
			}
		}
		return -1, router.TierConfig{Name: uniqueTierName(tiers, name, s.Tier), Cost: s.Tier, ProbeURL: s.Endpoint.BaseURL + "/models"}
	}
	for i, tier := range tiers {
		if tier.Cost == s.Tier && tier.ProbeURL == "" {
			return i, router.TierConfig{}
		}
	}
	name := map[string]string{router.CostFreeLocal: "local", router.CostFreeRemote: "remote", router.CostSubscription: "subscription", router.CostPaid: "paid"}[s.Tier]
	return -1, router.TierConfig{Name: uniqueTierName(tiers, cmp.Or(name, s.Tier), s.Tier), Cost: s.Tier}
}

func uniqueTierName(tiers []router.TierConfig, name, cost string) string {
	taken := func(n string) bool {
		return slices.ContainsFunc(tiers, func(t router.TierConfig) bool { return t.Name == n })
	}
	if !taken(name) {
		return name
	}
	if n := name + "-" + cost; !taken(n) {
		return n
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", name, i); !taken(n) {
			return n
		}
	}
}

// modelRef is the router entry for a setup model.
func (s *setupModel) modelRef() router.ModelRef {
	return router.ModelRef{Provider: s.Provider, Model: s.Model, Capability: s.Capability, Uncensored: s.Uncensored, PrivacySafe: s.PrivacySafe, Affinity: s.affinity(nil)}
}

// strengthBonus is the routing affinity a strength tag gives.
const strengthBonus = 0.1

// affinity is the router affinity for the model's strength tags: a tag the
// entry already weights keeps its weight, a new one gets strengthBonus.
func (s *setupModel) affinity(old map[string]float64) map[string]float64 {
	if len(s.Strengths) == 0 {
		return nil
	}
	out := map[string]float64{}
	for _, tag := range s.Strengths {
		out[tag] = cmp.Or(old[tag], strengthBonus)
	}
	return out
}

// mergeRouterJSON applies plan to src, the router.json text ("" when there
// is none): routing on or off and the classifier when chosen, and each new
// model in a tier. Models the file already routes keep their entries, and
// every other setting stays as written, except "mode", which is a session's
// choice now and is dropped.
func mergeRouterJSON(src string, plan routerPlan) (string, error) {
	if strings.TrimSpace(src) == "" {
		src = "{}\n"
	}
	var current struct {
		Tiers *[]router.TierConfig `json:"tiers"`
		Jev   json.RawMessage      `json:"jev"`
	}
	if err := json.Unmarshal([]byte(stripJSONComments(src)), &current); err != nil {
		return "", fmt.Errorf("router.json: %w", err)
	}
	set := func(src, key, value string) (string, error) {
		span, ok, err := jsoncFind(src, key)
		if err != nil {
			return "", err
		}
		if ok {
			return jsoncReplace(src, span, value), nil
		}
		root, _, err := jsoncFind(src)
		if err != nil {
			return "", err
		}
		return jsoncAppend(src, root, jsoncMember(key, value)), nil
	}
	var err error
	if plan.Enabled != nil {
		if src, err = set(src, "enabled", strconv.FormatBool(*plan.Enabled)); err != nil {
			return "", err
		}
	}
	if plan.Engine != "" {
		if src, err = set(src, "engine", strconv.Quote(plan.Engine)); err != nil {
			return "", err
		}
	}
	if span, ok, err := jsoncFind(src, "mode"); err != nil {
		return "", err
	} else if ok {
		src = jsoncRemove(src, span)
	}
	if plan.Jev != nil && len(current.Jev) == 0 {
		out, err := compactJSON(plan.Jev)
		if err != nil {
			return "", err
		}
		if src, err = set(src, "jev", out); err != nil {
			return "", err
		}
	} else if plan.Jev != nil {
		// Merge over the file's own classifier settings, so keys setup
		// doesn't ask about (retries, maxPromptChars) survive.
		merged := map[string]any{}
		if len(current.Jev) > 0 {
			_ = json.Unmarshal(current.Jev, &merged)
		}
		var chosen map[string]any
		data, _ := json.Marshal(plan.Jev)
		_ = json.Unmarshal(data, &chosen)
		maps.Copy(merged, chosen)
		if !plan.Jev.Disabled {
			delete(merged, "disabled")
		}
		if plan.Jev.APIKeyProvider == "" {
			// Unset picks the key automatically; drop an old "none".
			delete(merged, "apiKeyProvider")
		}
		if !plan.Jev.PrivacySafe {
			delete(merged, "privacySafe")
		}
		out, err := compactJSON(merged)
		if err != nil {
			return "", err
		}
		if src, err = set(src, "jev", out); err != nil {
			return "", err
		}
	}

	var tiers []router.TierConfig
	var counts []int // each file tier's model count before setup
	if current.Tiers != nil {
		tiers = slices.Clone(*current.Tiers)
		for _, tier := range tiers {
			counts = append(counts, len(tier.Models))
		}
	}
	// Edits and removals of models configured before setup: an edited
	// entry keeps its other settings (effort, affinity) and its place in
	// its tier unless its cost class changed.
	changedExisting := false
	for _, s := range plan.Existing {
		if !s.changed() {
			continue
		}
		changedExisting = true
		ref := s.modelRef()
		if s.Ref != nil {
			ref = *s.Ref
			ref.Capability, ref.Uncensored, ref.PrivacySafe, ref.Affinity = s.Capability, s.Uncensored, s.PrivacySafe, s.affinity(s.Ref.Affinity)
		}
		drop := s.Removed || s.NoRouting
		placed := false
		for i := range tiers {
			j := slices.IndexFunc(tiers[i].Models, func(r router.ModelRef) bool { return r.Spec() == s.spec() })
			if j < 0 {
				continue
			}
			if !drop && !placed && tiers[i].Cost == s.Tier {
				tiers[i].Models = slices.Clone(tiers[i].Models)
				tiers[i].Models[j] = ref
				placed = true
				continue
			}
			tiers[i].Models = slices.Delete(slices.Clone(tiers[i].Models), j, j+1)
		}
		if !drop && !placed {
			index, fresh := tierFor(tiers, s)
			if index < 0 {
				tiers = append(tiers, fresh)
				index = len(tiers) - 1
			}
			tiers[index].Models = append(slices.Clone(tiers[index].Models), ref)
		}
	}
	if changedExisting {
		tiers = slices.DeleteFunc(tiers, func(t router.TierConfig) bool { return len(t.Models) == 0 })
	}
	routed := func(spec string) bool {
		for _, tier := range tiers {
			if slices.ContainsFunc(tier.Models, func(r router.ModelRef) bool { return r.Spec() == spec }) {
				return true
			}
		}
		return false
	}
	var added []*setupModel
	for _, s := range plan.Models {
		if s.routable() && !s.NoRouting && !routed(s.spec()) {
			added = append(added, s)
			index, fresh := tierFor(tiers, s)
			if index < 0 {
				tiers = append(tiers, fresh)
				index = len(tiers) - 1
			}
			tiers[index].Models = append(tiers[index].Models, s.modelRef())
		}
	}
	if len(added) == 0 && current.Tiers != nil && !changedExisting {
		return src, nil
	}
	if current.Tiers == nil || changedExisting {
		// The file had no tiers, or entries changed: write the whole list.
		data, err := compactJSON(tiers)
		if err != nil {
			return "", err
		}
		return set(src, "tiers", data)
	}
	// Append in place: to an existing tier's model list, or a new tier.
	for i, tier := range tiers {
		if i >= len(counts) {
			data, err := compactJSON(tier)
			if err != nil {
				return "", err
			}
			if src, err = jsoncAppendAt(src, data, "tiers"); err != nil {
				return "", err
			}
			continue
		}
		for _, ref := range tier.Models[counts[i]:] {
			data, err := compactJSON(ref)
			if err != nil {
				return "", err
			}
			if src, err = jsoncAppendAt(src, data, "models", "tiers", fmt.Sprintf("#%d", i)); err != nil {
				return "", err
			}
		}
	}
	return src, nil
}

// updateProbeURL points the tiers probed at from to to, after an
// endpoint's address changed.
func updateProbeURL(src, from, to string) (string, error) {
	var current struct {
		Tiers []router.TierConfig `json:"tiers"`
	}
	if err := json.Unmarshal([]byte(src), &current); err != nil {
		return "", err
	}
	for i, tier := range current.Tiers {
		if tier.ProbeURL != from {
			continue
		}
		var err error
		if src, err = jsoncSet(src, strconv.Quote(to), "tiers", fmt.Sprintf("#%d", i), "probeUrl"); err != nil {
			return "", err
		}
	}
	return src, nil
}

// ─── router-speed.json ───────────────────────────────────────────────────

// mergeSpeedJSON seeds learned speeds: a model the router has learned
// from real work (more than the one sample a measurement seeds) keeps its
// averages and only gains a missing decode rate.
func mergeSpeedJSON(src string, measured map[string]router.SpeedStat) (string, error) {
	stats := map[string]router.SpeedStat{}
	if strings.TrimSpace(src) != "" {
		if err := json.Unmarshal([]byte(src), &stats); err != nil {
			return "", fmt.Errorf("%s: %w", router.StatsFileName, err)
		}
	}
	for spec, stat := range measured {
		if have, ok := stats[spec]; ok && have.Samples > 1 {
			if have.OutputTokensPerSecond == 0 {
				have.OutputTokensPerSecond = stat.OutputTokensPerSecond
				stats[spec] = have
			}
			continue
		}
		stats[spec] = stat
	}
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

// ─── Writing ─────────────────────────────────────────────────────────────

// readConfigFile returns a config file's text, "" when it doesn't exist.
func readConfigFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// writeConfigFile replaces path with content atomically. With backup, an
// existing file that changes is first copied to <path>.<timestamp>.bak,
// whose path is returned.
func writeConfigFile(path, content string, now time.Time, backup bool) (string, error) {
	old, err := readConfigFile(path)
	if err != nil {
		return "", err
	}
	if old == content {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	backupPath := ""
	if old != "" && backup {
		backupPath = path + "." + now.Format("20060102-150405") + ".bak"
		if err := os.WriteFile(backupPath, []byte(old), 0o600); err != nil {
			return "", err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return "", err
	}
	return backupPath, os.Rename(tmp, path)
}
