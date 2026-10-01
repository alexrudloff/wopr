package codingagent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/llama"
	"github.com/alexrudloff/wopr/internal/configvalue"
	"github.com/alexrudloff/wopr/internal/text"
)

// ─── Model Registry ──────────────────────────────────────────────────────────

// ModelEntry holds the resolved configuration for a single provider+model.
// Produced by ModelRegistry.Resolve from the models.json schema.
type ModelEntry struct {
	ProviderID       string
	ModelID          string
	APIKey           string
	BaseURL          string
	DisplayName      string
	API              string            // "openai-completions" | "anthropic-messages" | etc.
	Headers          map[string]string // resolved request headers from provider/model configuration
	ModelHeaders     map[string]string // intrinsic public Model.headers from the generated catalog
	AuthHeader       bool              // put API key in Authorization header (for custom providers)
	Compat           *ai.ModelCompat   // compat flags for OpenAI-compatible providers
	Reasoning        bool              // whether the model supports reasoning/thinking
	ThinkingLevelMap ai.ThinkingLevelMap
	SamplingParams   map[string]any
	InputLimits      *ai.ModelInputLimits
	Input            []string // ["text"] or ["text","image"]
	ContextWindow    int      // default: ai.DefaultContextWindow
	MaxTokens        int      // default: 16384
	InputCost        float64
	OutputCost       float64
	CacheReadCost    float64
	CacheWriteCost   float64
	CostTiers        []ai.CostTier
	PromptCache      ai.ModelPromptCache
	Env              map[string]string // provider-scoped env overrides (auth.json env)
	Insecure         bool              // skip TLS verification (self-signed/internal-CA on-prem endpoints)
}

// ─── models.json schema ──────────────────────────────────────────────────────

// modelsConfig is the top-level models.json schema.
// provider returns the models.json provider id; a nil config has none.
func (c *modelsConfig) provider(id string) (providerConfig, bool) {
	p, ok := c.providers()[id]
	return p, ok
}

// providers returns the models.json providers; a nil config has none.
func (c *modelsConfig) providers() map[string]providerConfig {
	if c == nil {
		return nil
	}
	return c.Providers
}

type modelsConfig struct {
	Providers map[string]providerConfig `json:"providers"`
}

// providerConfig is a provider entry in models.json.
type providerConfig struct {
	Name           string             `json:"name,omitempty"`
	BaseURL        string             `json:"baseUrl,omitempty"`
	APIKey         string             `json:"apiKey,omitempty"`
	API            string             `json:"api,omitempty"`
	Headers        map[string]*string `json:"headers,omitempty"`
	headerEntries  []orderedHeaderEntry
	Compat         *providerCompat              `json:"compat,omitempty"`
	AuthHeader     *bool                        `json:"authHeader,omitempty"`
	Models         []modelDefinition            `json:"models,omitempty"`
	ModelOverrides map[string]modelOverrideJSON `json:"modelOverrides,omitempty"`
	// OAuth allows extensions to register
	// providers with OAuth support (/login, token refresh, modifyModels).
	// The struct is stored but the actual login/refresh logic lives in the
	// extension process; wopr's model registry uses it for hasAuth checks and
	// model filtering.
	OAuth *oauthProviderConfig `json:"oauth,omitempty"`
	// Insecure skips TLS verification for this provider's endpoint. Opt-in,
	// for self-signed/internal-CA on-prem gateways.
	Insecure bool `json:"insecure,omitempty"`
}

// oauthProviderConfig captures the serializable parts of an OAuth
// provider registration. The actual login/refreshToken/getApiKey callbacks
// stay with the extension; this struct records whether a provider
// was registered with OAuth support so the model registry can perform
// hasAuth and getAvailable filtering.
type oauthProviderConfig struct {
	Name     string `json:"name,omitempty"`
	Kind     string `json:"-"`
	HasLogin bool   `json:"hasLogin,omitempty"`
}

func (config *oauthProviderConfig) UnmarshalJSON(data []byte) error {
	var kind string
	if json.Unmarshal(data, &kind) == nil {
		config.Kind = kind
		return nil
	}
	type plain oauthProviderConfig
	return json.Unmarshal(data, (*plain)(config))
}

// modelDefinition is a custom model entry within a provider.
type modelDefinition struct {
	ID               string               `json:"id"`
	Name             string               `json:"name,omitempty"`
	API              string               `json:"api,omitempty"`
	BaseURL          string               `json:"baseUrl,omitempty"`
	Reasoning        *bool                `json:"reasoning,omitempty"`
	ThinkingLevelMap ai.ThinkingLevelMap  `json:"thinkingLevelMap,omitempty"`
	SamplingParams   map[string]any       `json:"samplingParams,omitempty"`
	InputLimits      *ai.ModelInputLimits `json:"inputLimits,omitempty"`
	Input            *[]string            `json:"input,omitempty"`
	Cost             *modelCostJSON       `json:"cost,omitempty"`
	PromptCache      ai.ModelPromptCache  `json:"promptCache,omitempty"`
	ContextWindow    *int                 `json:"contextWindow,omitempty"`
	MaxTokens        *int                 `json:"maxTokens,omitempty"`
	Headers          map[string]*string   `json:"headers,omitempty"`
	headerEntries    []orderedHeaderEntry
	Compat           *providerCompat `json:"compat,omitempty"`
}

type modelCostJSON struct {
	Input      float64       `json:"input"`
	Output     float64       `json:"output"`
	CacheRead  float64       `json:"cacheRead"`
	CacheWrite float64       `json:"cacheWrite"`
	Tiers      []ai.CostTier `json:"tiers,omitempty"`
}

type modelCostOverrideJSON struct {
	Input      *float64       `json:"input,omitempty"`
	Output     *float64       `json:"output,omitempty"`
	CacheRead  *float64       `json:"cacheRead,omitempty"`
	CacheWrite *float64       `json:"cacheWrite,omitempty"`
	Tiers      *[]ai.CostTier `json:"tiers,omitempty"`
}

type modelOverrideJSON struct {
	Name             string                 `json:"name,omitempty"`
	Reasoning        *bool                  `json:"reasoning,omitempty"`
	ThinkingLevelMap ai.ThinkingLevelMap    `json:"thinkingLevelMap,omitempty"`
	SamplingParams   map[string]any         `json:"samplingParams,omitempty"`
	InputLimits      *ai.ModelInputLimits   `json:"inputLimits,omitempty"`
	Input            *[]string              `json:"input,omitempty"`
	Cost             *modelCostOverrideJSON `json:"cost,omitempty"`
	PromptCache      ai.ModelPromptCache    `json:"promptCache,omitempty"`
	ContextWindow    *int                   `json:"contextWindow,omitempty"`
	MaxTokens        *int                   `json:"maxTokens,omitempty"`
	Headers          map[string]*string     `json:"headers,omitempty"`
	headerEntries    []orderedHeaderEntry
	Compat           *providerCompat `json:"compat,omitempty"`
}

type orderedHeaderEntry struct {
	Name  string
	Value *string
}

type providerConfigJSON providerConfig

type modelDefinitionJSON modelDefinition

type modelOverrideJSONAlias modelOverrideJSON

func (config *providerConfig) UnmarshalJSON(data []byte) error {
	decoded, entries, err := unmarshalConfigJSON[providerConfigJSON](data, false)
	if err != nil {
		return err
	}
	*config = providerConfig(decoded)
	config.headerEntries = entries
	return nil
}

func (definition *modelDefinition) UnmarshalJSON(data []byte) error {
	decoded, entries, err := unmarshalConfigJSON[modelDefinitionJSON](data, true)
	if err != nil {
		return err
	}
	*definition = modelDefinition(decoded)
	definition.headerEntries = entries
	return nil
}

func (override *modelOverrideJSON) UnmarshalJSON(data []byte) error {
	decoded, entries, err := unmarshalConfigJSON[modelOverrideJSONAlias](data, true)
	if err != nil {
		return err
	}
	*override = modelOverrideJSON(decoded)
	override.headerEntries = entries
	return nil
}

// unmarshalConfigJSON decodes data as A, a configuration type without its
// UnmarshalJSON, normalizing input limits first when normalize is set, and
// returns it with the headers in file order.
func unmarshalConfigJSON[A any](data []byte, normalize bool) (A, []orderedHeaderEntry, error) {
	var decoded A
	source := data
	if normalize {
		var err error
		if source, err = normalizeModelInputLimitsJSON(data); err != nil {
			return decoded, nil, err
		}
	}
	if err := json.Unmarshal(source, &decoded); err != nil {
		return decoded, nil, err
	}
	entries, err := decodeOrderedHeaderEntries(data)
	return decoded, entries, err
}

func decodeOrderedHeaderEntries(data []byte) ([]orderedHeaderEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("model configuration must be an object")
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if key != "headers" {
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return nil, err
			}
			continue
		}
		return decodeOrderedHeaderObject(decoder)
	}
	return nil, nil
}

func decodeOrderedHeaderObject(decoder *json.Decoder) ([]orderedHeaderEntry, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("headers must be an object")
	}
	var entries []orderedHeaderEntry
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value *string
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		entries = append(entries, orderedHeaderEntry{Name: name.(string), Value: value})
	}
	_, err = decoder.Token()
	return entries, err
}

// providerCompat is a provider's compat block in models.json.
type providerCompat ai.ModelCompat

// ─── ModelRegistry ────────────────────────────────────────────────────────────

// ModelRegistry resolves model configurations from models.json and environment.
type ModelRegistry struct {
	mu        sync.RWMutex
	agentDir  string
	config    *modelsConfig // parsed models.json (nil if absent/invalid)
	loadError string        // non-empty if models.json failed to parse
	dynamic   map[string]providerConfig

	// authStorage, when non-nil, provides credential lookup for
	// HasConfiguredAuth and GetAvailable filtering.
	authStorage *ai.AuthStorage
	onChange    *modelRegistryChangeListener

	// runtimeCredentials overlays non-persistent API keys (--api-key) on
	// authStorage; they outrank stored and ambient credentials.
	runtimeCredentials *ai.RuntimeCredentials
}

type modelRegistryChangeListener struct {
	mu     sync.Mutex
	active bool
	notify func()
}

func (listener *modelRegistryChangeListener) publish() {
	if listener == nil {
		return
	}
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if listener.active {
		listener.notify()
	}
}

func (listener *modelRegistryChangeListener) close() {
	if listener == nil {
		return
	}
	listener.mu.Lock()
	listener.active = false
	listener.mu.Unlock()
}

// NewModelRegistry creates a registry, loading from <agent-dir>/models.json if present.
func NewModelRegistry(agentDir string) *ModelRegistry {
	r := &ModelRegistry{agentDir: agentDir}
	r.load()
	return r
}

func (r *ModelRegistry) readConfig() (*modelsConfig, string) {
	data, err := os.ReadFile(filepath.Join(r.agentDir, "models.json"))
	if err != nil {
		return nil, "" // No models.json is fine
	}
	var cfg modelsConfig
	if err := json.Unmarshal([]byte(stripJSONComments(text.StripBom(string(data)))), &cfg); err != nil {
		return nil, "Failed to parse models.json: " + err.Error()
	}
	if err := r.validate(&cfg); err != nil {
		return nil, err.Error()
	}
	return &cfg, strings.Join(dropUncomposableProviders(&cfg), "\n\n")
}

// dropUncomposableProviders removes models.json providers that set "oauth"
// without "baseUrl" and reports them. Such a provider cannot be composed; the
// runtime records the composition error and keeps the base.
func dropUncomposableProviders(config *modelsConfig) []string {
	var failures []string
	for providerID, provider := range config.Providers {
		if provider.OAuth != nil && provider.OAuth.Kind != "" && provider.BaseURL == "" {
			failures = append(failures, fmt.Sprintf("Provider %q: Provider %s: \"baseUrl\" is required when \"oauth\" is set.", providerID, providerID))
			delete(config.Providers, providerID)
		}
	}
	slices.Sort(failures)
	return failures
}

func (r *ModelRegistry) load() {
	r.config, r.loadError = r.readConfig()
}

// validate checks models.json for common errors.
func (r *ModelRegistry) validate(cfg *modelsConfig) error {
	for name, prov := range cfg.Providers {
		if len(prov.Models) == 0 && prov.BaseURL == "" && prov.APIKey == "" && len(prov.Headers) == 0 && prov.Compat == nil && len(prov.ModelOverrides) == 0 && prov.OAuth == nil && prov.AuthHeader == nil {
			return fmt.Errorf("provider %q: must specify \"baseUrl\", \"headers\", \"compat\", \"modelOverrides\", \"models\", \"apiKey\", \"oauth\", or \"authHeader\"", name)
		}
		if len(prov.Models) > 0 && prov.BaseURL == "" {
			// Non-built-in providers with models require baseUrl.
			// (Built-in providers inherit from their registration.)
			if !isBuiltInProvider(name) {
				return fmt.Errorf("provider %q: \"baseUrl\" is required when defining custom models", name)
			}
		}
		for i, md := range prov.Models {
			if md.ID == "" {
				return fmt.Errorf("provider %q: model at index %d: \"id\" is required", name, i)
			}
		}
	}
	return nil
}

// isBuiltInProvider returns true for providers with hardcoded defaults in buildModel.
func isBuiltInProvider(name string) bool {
	switch name {
	case "openai", "github-copilot", "anthropic", "openrouter", "together", "groq", "ollama":
		return true
	}
	return false
}

// LoadError returns any error from parsing models.json.
func (r *ModelRegistry) LoadError() string { return r.loadError }

// Resolve returns a ModelEntry for the given provider+model combination.
// Resolution order:
//  1. Custom model definition in models.json (provider → models[])
//  2. Model override in models.json (provider → modelOverrides[modelID])
//  3. Provider-level defaults from models.json (baseUrl, apiKey, compat, headers)
//  4. Environment variables (PROVIDER_API_KEY)
func (r *ModelRegistry) Resolve(providerID, modelID string) (ModelEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var configured *providerConfig
	if provider, ok := r.config.provider(providerID); ok {
		configured = &provider
	}
	if dynamic, ok := r.dynamic[providerID]; ok {
		if dynamic.Models != nil {
			definition, found := findModelDefinition(dynamic.Models, modelID)
			if !found {
				entry := r.resolveProviderDefaults(providerID, dynamic)
				entry.ModelID = modelID
				return entry, true
			}
			entry := r.resolveModelDef(providerID, dynamic, definition)
			if override, exists := dynamic.ModelOverrides[modelID]; exists {
				r.applyOverride(&entry, override)
			}
			if configured != nil {
				if override, exists := configured.ModelOverrides[modelID]; exists {
					r.applyOverride(&entry, override)
				}
			}
			entry.Headers = r.composeRequestHeadersLocked(nil, providerID, modelID, configured, &dynamic)
			return entry, true
		}
		entry := r.resolveProviderDefaults(providerID, dynamic)
		entry.ModelID = modelID
		if configured != nil {
			entry = r.mergeProviderDefaultsLocked(entry, *configured)
			if definition, found := findModelDefinition(configured.Models, modelID); found {
				entry = r.resolveModelDef(providerID, *configured, definition)
			}
			if override, exists := configured.ModelOverrides[modelID]; exists {
				r.applyOverride(&entry, override)
			}
		}
		entry.Headers = r.composeRequestHeadersLocked(nil, providerID, modelID, configured, &dynamic)
		return entry, true
	}
	if configured != nil {
		if definition, found := findModelDefinition(configured.Models, modelID); found {
			entry := r.resolveModelDef(providerID, *configured, definition)
			if override, exists := configured.ModelOverrides[modelID]; exists {
				r.applyOverride(&entry, override)
			}
			entry.Headers = r.composeRequestHeadersLocked(nil, providerID, modelID, configured, nil)
			return entry, true
		}
		if override, exists := configured.ModelOverrides[modelID]; exists {
			if _, generated := ai.LookupModelExact(providerID + "/" + modelID); generated {
				entry := r.resolveProviderDefaults(providerID, *configured)
				entry.ModelID = modelID
				r.applyOverride(&entry, override)
				return entry, true
			}
		}
		if providerHasDefaults(*configured) {
			entry := r.resolveProviderDefaults(providerID, *configured)
			entry.ModelID = modelID
			entry.Headers = r.composeRequestHeadersLocked(nil, providerID, modelID, configured, nil)
			return entry, true
		}
	}
	if hasEnvAuth(providerID) {
		return ModelEntry{ProviderID: providerID, ModelID: modelID, APIKey: resolveAPIKeyFromEnv(providerID)}, true
	}
	return ModelEntry{}, false
}

func findModelDefinition(models []modelDefinition, modelID string) (modelDefinition, bool) {
	for _, definition := range models {
		if definition.ID == modelID {
			return definition, true
		}
	}
	return modelDefinition{}, false
}

func providerHasDefaults(provider providerConfig) bool {
	return provider.BaseURL != "" || provider.APIKey != "" || provider.API != "" || provider.Compat != nil || len(provider.Headers) > 0 || provider.AuthHeader != nil || provider.OAuth != nil
}

// ResolveCatalogModel layers provider configuration and a matching model override onto one generated model.
func (r *ModelRegistry) ResolveCatalogModel(providerID, modelID string, generated *ai.KnownModel) ModelEntry {
	entry := catalogModelEntry(providerID, modelID, generated)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var configured *providerConfig
	if provider, ok := r.config.provider(providerID); ok {
		configured = &provider
		entry = r.resolveCatalogModelLocked(entry, provider)
	}
	if dynamic, ok := r.dynamic[providerID]; ok {
		if dynamic.Models != nil {
			if definition, found := findModelDefinition(dynamic.Models, modelID); found {
				fallbackAPI, fallbackBaseURL := entry.API, entry.BaseURL
				entry = r.resolveModelDef(providerID, dynamic, definition)
				entry.API = cmp.Or(entry.API, fallbackAPI)
				entry.BaseURL = cmp.Or(entry.BaseURL, fallbackBaseURL)
			}
		} else {
			entry.BaseURL = cmp.Or(dynamic.BaseURL, entry.BaseURL)
			entry.Headers = mergeHeadersOrdered(entry.Headers, dynamic.Headers, dynamic.headerEntries, r.providerEnv(providerID))
			if dynamic.APIKey != "" {
				entry.APIKey = configvalue.Resolve(dynamic.APIKey, r.providerEnv(providerID))
			}
			entry.Insecure = dynamic.Insecure
		}
	}
	if configured != nil {
		if override, ok := configured.ModelOverrides[modelID]; ok {
			r.applyOverride(&entry, override)
		}
	}
	var dynamic *providerConfig
	if registered, ok := r.dynamic[providerID]; ok {
		dynamic = &registered
	}
	entry.Headers = r.composeRequestHeadersLocked(entry.ModelHeaders, providerID, modelID, configured, dynamic)
	if entry.APIKey == "" {
		entry.APIKey = resolveAPIKeyFromEnv(providerID)
	}
	return entry
}

func (r *ModelRegistry) composeRequestHeadersLocked(base map[string]string, providerID, modelID string, configured, dynamic *providerConfig) map[string]string {
	env := r.providerEnv(providerID)
	headers := maps.Clone(base)
	if configured != nil {
		headers = mergeHeadersOrdered(headers, configured.Headers, configured.headerEntries, env)
	}
	if dynamic != nil {
		headers = mergeHeadersOrdered(headers, dynamic.Headers, dynamic.headerEntries, env)
	}
	if configured != nil {
		if override, ok := configured.ModelOverrides[modelID]; ok {
			headers = mergeHeadersOrdered(headers, override.Headers, override.headerEntries, env)
		}
		if definition, ok := findModelDefinition(configured.Models, modelID); ok {
			headers = mergeHeadersOrdered(headers, definition.Headers, definition.headerEntries, env)
		}
	}
	if dynamic != nil {
		if definition, ok := findModelDefinition(dynamic.Models, modelID); ok {
			headers = mergeHeadersOrdered(headers, definition.Headers, definition.headerEntries, env)
		}
	}
	return headers
}

func catalogModelEntry(providerID, modelID string, generated *ai.KnownModel) ModelEntry {
	return ModelEntry{
		ProviderID:       providerID,
		ModelID:          modelID,
		BaseURL:          generated.BaseURL,
		DisplayName:      generated.DisplayName,
		API:              string(generated.API),
		Headers:          maps.Clone(generated.Headers),
		ModelHeaders:     maps.Clone(generated.Headers),
		Compat:           mergeCompat((*providerCompat)(generated.Compat), nil),
		Reasoning:        generated.Reasoning,
		ThinkingLevelMap: cloneThinkingLevelMap(generated.ThinkingLevelMap),
		SamplingParams:   maps.Clone(generated.SamplingParams),
		Input:            append([]string(nil), generated.Capabilities...),
		InputLimits:      generated.InputLimits.Clone(),
		ContextWindow:    generated.ContextWindow,
		MaxTokens:        generated.MaxOutputTokens,
		InputCost:        generated.InputCostPerMTokens,
		OutputCost:       generated.OutputCostPerMTokens,
		CacheReadCost:    generated.CacheReadCost,
		CacheWriteCost:   generated.CacheWriteCost,
		CostTiers:        append([]ai.CostTier(nil), generated.Tiers...),
		PromptCache:      maps.Clone(generated.PromptCache),
	}
}

func (r *ModelRegistry) resolveCatalogModelLocked(entry ModelEntry, provider providerConfig) ModelEntry {
	entry.BaseURL = cmp.Or(provider.BaseURL, entry.BaseURL)
	entry.Headers = mergeHeadersOrdered(entry.Headers, provider.Headers, provider.headerEntries, r.providerEnv(entry.ProviderID))
	entry.Compat = mergeCompat((*providerCompat)(entry.Compat), provider.Compat)
	entry.Insecure = provider.Insecure
	entry.AuthHeader = provider.AuthHeader != nil && *provider.AuthHeader
	entry.APIKey = configvalue.Resolve(provider.APIKey, r.providerEnv(entry.ProviderID))
	if entry.APIKey == "" {
		entry.APIKey = resolveAPIKeyFromEnv(entry.ProviderID)
	}
	for _, definition := range provider.Models {
		if definition.ID != entry.ModelID {
			continue
		}
		generatedAPI := entry.API
		generatedBaseURL := entry.BaseURL
		entry = r.resolveModelDef(entry.ProviderID, provider, definition)
		entry.API = cmp.Or(entry.API, generatedAPI)
		entry.BaseURL = cmp.Or(entry.BaseURL, generatedBaseURL)
		break
	}
	if override, ok := provider.ModelOverrides[entry.ModelID]; ok {
		r.applyOverride(&entry, override)
	}
	return entry
}

func (r *ModelRegistry) mergeProviderDefaultsLocked(entry ModelEntry, provider providerConfig) ModelEntry {
	entry.BaseURL = cmp.Or(entry.BaseURL, provider.BaseURL)
	entry.API = cmp.Or(entry.API, provider.API)
	if entry.APIKey == "" {
		entry.APIKey = configvalue.Resolve(provider.APIKey, r.providerEnv(entry.ProviderID))
	}
	entry.Headers = mergeHeadersOrdered(entry.Headers, provider.Headers, provider.headerEntries, r.providerEnv(entry.ProviderID))
	entry.Compat = mergeCompat((*providerCompat)(entry.Compat), provider.Compat)
	if provider.AuthHeader != nil {
		entry.AuthHeader = *provider.AuthHeader
	}
	entry.Insecure = entry.Insecure || provider.Insecure
	return entry
}

// HasModelDefinition reports whether provider composition exposes providerID/modelID through an explicit models list.
func (r *ModelRegistry) HasModelDefinition(providerID, modelID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if dynamic, ok := r.dynamic[providerID]; ok {
		if dynamic.Models != nil {
			return providerDefinesModel(dynamic, modelID)
		}
	}
	provider, ok := r.config.provider(providerID)
	return ok && providerDefinesModel(provider, modelID)
}

// HasCatalogModel reports whether provider composition retains one generated identity.
func (r *ModelRegistry) HasCatalogModel(providerID, modelID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if dynamic, ok := r.dynamic[providerID]; ok && dynamic.Models != nil {
		return providerDefinesModel(dynamic, modelID)
	}
	_, ok := ai.LookupModelExact(providerID + "/" + modelID)
	return ok
}

// providerDefinesModel reports whether prov declares modelID in its models list.
func providerDefinesModel(prov providerConfig, modelID string) bool {
	_, ok := findModelDefinition(prov.Models, modelID)
	return ok
}

// SetProvider replaces a runtime-registered provider in one committed change,
// so fields absent from config are cleared rather than kept. The built-in
// llama.cpp provider republishes its resolved auth and catalog this way.
func (r *ModelRegistry) SetProvider(name string, configMap llama.ProviderConfig) {
	provider, ok := providerConfigFromRegistration(configMap)
	if !ok {
		return
	}
	r.mu.Lock()
	if r.dynamic == nil {
		r.dynamic = make(map[string]providerConfig)
	}
	r.dynamic[name] = provider
	listener := r.onChange
	r.mu.Unlock()
	listener.publish()
}

func providerConfigFromRegistration(configMap llama.ProviderConfig) (providerConfig, bool) {
	data, err := json.Marshal(configMap)
	if err != nil {
		return providerConfig{}, false
	}
	var provider providerConfig
	if err := json.Unmarshal(data, &provider); err != nil {
		return providerConfig{}, false
	}
	if configMap.Models != nil && provider.Models == nil {
		provider.Models = []modelDefinition{}
	}
	return provider, true
}

// SetAuthStorage wires the credential store for auth-aware filtering.
// Must be called before GetAvailable or HasConfiguredAuth produce
// meaningful results.
func (r *ModelRegistry) SetAuthStorage(auth *ai.AuthStorage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authStorage = auth
	r.runtimeCredentials = nil
}

// runtimeCredentialsLocked returns the runtime key overlay over authStorage.
// The caller holds r.mu for writing.
func (r *ModelRegistry) runtimeCredentialsLocked() *ai.RuntimeCredentials {
	if r.runtimeCredentials == nil {
		var store ai.CredentialStore
		if r.authStorage != nil {
			store = r.authStorage
		}
		r.runtimeCredentials = ai.NewRuntimeCredentials(store)
	}
	return r.runtimeCredentials
}

// SetRuntimeAPIKey installs a non-persistent API key for providerID. It acts
// as a stored api_key credential that is never written to auth.json.
func (r *ModelRegistry) SetRuntimeAPIKey(providerID, apiKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtimeCredentialsLocked().SetRuntimeAPIKey(providerID, apiKey)
}

// RemoveRuntimeAPIKey drops providerID's runtime key.
func (r *ModelRegistry) RemoveRuntimeAPIKey(providerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimeCredentials != nil {
		r.runtimeCredentials.RemoveRuntimeAPIKey(providerID)
	}
}

// RuntimeAPIKey returns the non-persistent API key set for providerID.
func (r *ModelRegistry) RuntimeAPIKey(providerID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.runtimeCredentials == nil {
		return "", false
	}
	return r.runtimeCredentials.RuntimeAPIKey(providerID)
}

// providerEnv returns the provider-scoped environment overrides for a
// provider's API-key credential, or nil. These take precedence over the
// process environment when resolving config-value `$VAR` references for the
// provider's API key and headers on the request-auth path.
func (r *ModelRegistry) providerEnv(providerID string) map[string]string {
	if r.authStorage == nil {
		return nil
	}
	env, err := r.authStorage.GetProviderEnv(providerID)
	if err != nil {
		return nil
	}
	return env
}

// Refresh reloads models.json from disk and preserves dynamic provider
// registrations.
func (r *ModelRegistry) Refresh() {
	r.refreshContext(context.Background())
}

// SetChangeListener replaces the callback invoked after a committed registry change and returns a draining detach function.
func (r *ModelRegistry) SetChangeListener(notify func()) func() {
	var listener *modelRegistryChangeListener
	if notify != nil {
		listener = &modelRegistryChangeListener{active: true, notify: notify}
	}
	r.mu.Lock()
	previous := r.onChange
	r.onChange = listener
	r.mu.Unlock()
	previous.close()
	return func() {
		r.mu.Lock()
		if r.onChange == listener {
			r.onChange = nil
		}
		r.mu.Unlock()
		listener.close()
	}
}

// refreshContext parses outside the registry lock and publishes the complete
// configuration only if the caller still owns the operation. It reports
// whether ctx ended first.
func (r *ModelRegistry) refreshContext(ctx context.Context) (aborted bool) {
	if ctx.Err() != nil {
		return true
	}
	config, loadError := r.readConfig()
	r.mu.Lock()
	if ctx.Err() != nil {
		r.mu.Unlock()
		return true
	}
	r.config = config
	r.loadError = loadError
	listener := r.onChange
	r.mu.Unlock()
	listener.publish()
	return false
}

// GetAll returns explicit models and configured overlays for exact generated identities; dynamic providers take precedence over models.json.
func (r *ModelRegistry) GetAll() []ModelEntry {
	out := r.getAllConfigured()
	sortModelEntries(out)
	return out
}

func sortModelEntries(entries []ModelEntry) {
	slices.SortFunc(entries, func(a, b ModelEntry) int {
		return cmp.Or(strings.Compare(a.ProviderID, b.ProviderID), strings.Compare(a.ModelID, b.ModelID))
	})
}

func (r *ModelRegistry) getAllConfigured() []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ModelEntry
	appendProvider := func(providerID string, provider providerConfig, overrides map[string]modelOverrideJSON, includeGeneratedOverrides bool) {
		seen := make(map[string]struct{}, len(provider.Models))
		for _, model := range provider.Models {
			entry := r.resolveModelDef(providerID, provider, model)
			if override, ok := overrides[model.ID]; ok {
				r.applyOverride(&entry, override)
			}
			out = append(out, entry)
			seen[model.ID] = struct{}{}
		}
		if !includeGeneratedOverrides {
			return
		}
		for modelID, override := range overrides {
			if _, explicit := seen[modelID]; explicit {
				continue
			}
			if _, generated := ai.LookupModelExact(providerID + "/" + modelID); !generated {
				continue
			}
			entry := r.resolveProviderDefaults(providerID, provider)
			entry.ModelID = modelID
			r.applyOverride(&entry, override)
			out = append(out, entry)
		}
	}
	for providerID, dynamic := range r.dynamic {
		configured, _ := r.config.provider(providerID)
		appendProvider(providerID, dynamic, configured.ModelOverrides, false)
	}
	for providerID, provider := range r.config.providers() {
		if dynamic, shadowed := r.dynamic[providerID]; shadowed && dynamic.Models != nil {
			continue
		}
		appendProvider(providerID, provider, provider.ModelOverrides, true)
	}
	return out
}

// GetAvailable returns all model entries from dynamic (runtime-registered, e.g. llama.cpp)
// and models.json providers that have configured auth (API key or OAuth
// credentials). It filters a single model list merged from
// built-ins, models.json custom overlays, and runtime overlays --
// not just runtime-registered providers. A provider name registered
// dynamically takes precedence over a models.json entry of the same name,
// matching Resolve()'s lookup order.
func (r *ModelRegistry) GetAvailable() []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ModelEntry
	for providerID, prov := range r.dynamic {
		if !r.hasConfiguredAuthLocked(providerID, &prov) {
			continue
		}
		for _, md := range prov.Models {
			out = append(out, r.resolveModelDef(providerID, prov, md))
		}
	}
	for providerID, prov := range r.config.providers() {
		if _, dynamicOwns := r.dynamic[providerID]; dynamicOwns {
			continue
		}
		if !r.hasConfiguredAuthLocked(providerID, &prov) {
			continue
		}
		for _, md := range prov.Models {
			out = append(out, r.resolveModelDef(providerID, prov, md))
		}
	}
	return out
}

// HasConfiguredAuth reports whether the provider has either an API key
// (from env, models.json, or dynamic registration) or stored OAuth
// credentials.
func (r *ModelRegistry) HasConfiguredAuth(providerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.runtimeCredentials != nil && r.runtimeCredentials.HasRuntimeAPIKey(providerID) {
		return true
	}
	if prov, ok := r.dynamic[providerID]; ok {
		return r.hasConfiguredAuthLocked(providerID, &prov)
	}
	if prov, ok := r.config.provider(providerID); ok {
		return r.hasConfiguredAuthLocked(providerID, &prov)
	}
	return r.HasAnyKey(providerID) || r.hasStoredCredential(providerID)
}

func (r *ModelRegistry) GetProviderAuthStatus(providerID string) ai.AuthStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.authStorage != nil {
		status := r.authStorage.GetAuthStatus(providerID)
		if status.Source != "" {
			return status
		}
	}
	if prov, ok := r.dynamic[providerID]; ok && prov.APIKey != "" {
		if configvalue.IsCommandConfigValue(prov.APIKey) {
			return ai.AuthStatus{Configured: true, Source: ai.AuthSourceModelsJSONCommand}
		}
		if envVarNames := configvalue.GetConfigValueEnvVarNames(prov.APIKey); len(envVarNames) > 0 {
			if configvalue.IsConfigValueConfigured(prov.APIKey, r.providerEnv(providerID)) {
				return ai.AuthStatus{Configured: true, Source: ai.AuthSourceEnvironment, Label: strings.Join(envVarNames, ", ")}
			}
			return ai.AuthStatus{Configured: false}
		}
		return ai.AuthStatus{Configured: true, Source: ai.AuthSourceModelsJSONKey}
	}
	return ai.AuthStatus{}
}

var builtInProviderDisplayNames = map[string]string{
	"anthropic":              "Anthropic",
	"amazon-bedrock":         "Amazon Bedrock",
	"ant-ling":               "Ant Ling",
	"azure-openai-responses": "Azure OpenAI Responses",
	"cerebras":               "Cerebras",
	"cloudflare-ai-gateway":  "Cloudflare AI Gateway",
	"cloudflare-workers-ai":  "Cloudflare Workers AI",
	"deepseek":               "DeepSeek",
	"fireworks":              "Fireworks",
	"google":                 "Google Gemini",
	"google-vertex":          "Google Vertex AI",
	"groq":                   "Groq",
	"huggingface":            "Hugging Face",
	"kimi-coding":            "Kimi For Coding",
	"mistral":                "Mistral",
	"minimax":                "MiniMax",
	"minimax-cn":             "MiniMax (China)",
	"moonshotai":             "Moonshot AI",
	"moonshotai-cn":          "Moonshot AI (China)",
	"opencode":               "OpenCode Zen",
	"opencode-go":            "OpenCode Go",
	"openai":                 "OpenAI",
	"openrouter":             "OpenRouter",
	"together":               "Together AI",
	"vercel-ai-gateway":      "Vercel AI Gateway",
	"xai":                    "xAI",
	"xiaomi-token-plan-ams":  "Xiaomi MiMo Token Plan (Amsterdam)",
	"xiaomi-token-plan-cn":   "Xiaomi MiMo Token Plan (China)",
	"xiaomi-token-plan-sgp":  "Xiaomi MiMo Token Plan (Singapore)",
	"zai":                    "ZAI Coding Plan (Global)",
	"zai-coding-cn":          "ZAI Coding Plan (China)",
}

func (r *ModelRegistry) GetProviderDisplayName(providerID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if prov, ok := r.dynamic[providerID]; ok {
		if prov.Name != "" {
			return prov.Name
		}
		if prov.OAuth != nil && prov.OAuth.Name != "" {
			return prov.OAuth.Name
		}
	}
	if r.config != nil {
		if prov, ok := r.config.Providers[providerID]; ok && prov.Name != "" {
			return prov.Name
		}
	}
	if name, ok := builtInProviderDisplayNames[providerID]; ok {
		return name
	}
	return providerID
}

func (r *ModelRegistry) hasConfiguredAuthLocked(providerID string, prov *providerConfig) bool {
	// Explicit authHeader:false declares an endpoint that does not require an
	// API key. It must remain selectable and callable without fabricated auth.
	if prov.AuthHeader != nil && !*prov.AuthHeader {
		return true
	}
	// Dynamic provider with explicit API key.
	if prov.APIKey != "" && configvalue.Resolve(prov.APIKey, r.providerEnv(providerID)) != "" {
		return true
	}
	// OAuth registration present → check stored credentials.
	if prov.OAuth != nil && prov.OAuth.HasLogin {
		return r.hasStoredOAuth(providerID)
	}
	// Fall back to a stored credential or env-based auth.
	return hasEnvAuth(providerID) || r.hasStoredCredential(providerID)
}

func (r *ModelRegistry) hasStoredOAuth(providerID string) bool {
	if r.authStorage == nil {
		return false
	}
	cred, ok, _ := r.authStorage.Get(providerID)
	return ok && cred.Type == "oauth"
}

// hasStoredCredential reports any stored auth.json credential for
// providerID. A stored api_key or oauth credential counts as configured auth.
func (r *ModelRegistry) hasStoredCredential(providerID string) bool {
	if r.authStorage == nil {
		return false
	}
	_, ok, _ := r.authStorage.GetRaw(providerID)
	return ok
}

// resolveModelDef builds a ModelEntry from a custom model definition.
func (r *ModelRegistry) resolveModelDef(providerID string, prov providerConfig, md modelDefinition) ModelEntry {
	// Model-level overrides provider-level.
	baseURL := cmp.Or(md.BaseURL, prov.BaseURL)
	api := cmp.Or(md.API, prov.API)
	env := r.providerEnv(providerID)
	apiKey := configvalue.Resolve(prov.APIKey, env)

	// Merge headers: provider → model.
	providerHeaders := mergeHeadersOrdered(nil, prov.Headers, prov.headerEntries, env)
	headers := mergeHeadersOrdered(providerHeaders, md.Headers, md.headerEntries, env)

	// Merge compat: provider → model (model wins per-field).
	compat := mergeCompat(prov.Compat, md.Compat)

	name := cmp.Or(md.Name, md.ID)

	reasoning := false
	if md.Reasoning != nil {
		reasoning = *md.Reasoning
	}

	input := []string{"text"}
	if md.Input != nil {
		input = append([]string{}, (*md.Input)...)
	}

	ctxWindow := ai.DefaultContextWindow
	if md.ContextWindow != nil {
		ctxWindow = *md.ContextWindow
	}

	maxTokens := 16384
	if md.MaxTokens != nil {
		maxTokens = *md.MaxTokens
	}
	var inputCost, outputCost, cacheReadCost, cacheWriteCost float64
	var costTiers []ai.CostTier
	if md.Cost != nil {
		inputCost = md.Cost.Input
		outputCost = md.Cost.Output
		cacheReadCost = md.Cost.CacheRead
		cacheWriteCost = md.Cost.CacheWrite
		costTiers = append([]ai.CostTier(nil), md.Cost.Tiers...)
	}

	return ModelEntry{
		ProviderID:       providerID,
		ModelID:          md.ID,
		APIKey:           apiKey,
		BaseURL:          baseURL,
		DisplayName:      name,
		API:              api,
		Headers:          headers,
		Compat:           compat,
		Reasoning:        reasoning,
		ThinkingLevelMap: cloneThinkingLevelMap(md.ThinkingLevelMap),
		SamplingParams:   maps.Clone(md.SamplingParams),
		Input:            input,
		InputLimits:      md.InputLimits.Clone(),
		ContextWindow:    ctxWindow,
		MaxTokens:        maxTokens,
		InputCost:        inputCost,
		OutputCost:       outputCost,
		CacheReadCost:    cacheReadCost,
		CacheWriteCost:   cacheWriteCost,
		CostTiers:        costTiers,
		PromptCache:      maps.Clone(md.PromptCache),
		Env:              env,
		Insecure:         prov.Insecure,
	}
}

// resolveProviderDefaults builds a partial ModelEntry from provider-level config.
func (r *ModelRegistry) resolveProviderDefaults(providerID string, prov providerConfig) ModelEntry {
	env := r.providerEnv(providerID)
	apiKey := configvalue.Resolve(prov.APIKey, env)
	authHeader := false
	if prov.AuthHeader != nil {
		authHeader = *prov.AuthHeader
	}
	return ModelEntry{
		ProviderID: providerID,
		BaseURL:    prov.BaseURL,
		APIKey:     apiKey,
		API:        prov.API,
		Headers:    mergeHeadersOrdered(nil, prov.Headers, prov.headerEntries, env),
		Env:        env,
		AuthHeader: authHeader,
		Compat:     mergeCompat(prov.Compat, nil),
		Insecure:   prov.Insecure,
	}
}

// applyOverride merges a modelOverride into an existing ModelEntry.
func (r *ModelRegistry) applyOverride(e *ModelEntry, ovr modelOverrideJSON) {
	e.DisplayName = cmp.Or(ovr.Name, e.DisplayName)
	if ovr.Reasoning != nil {
		e.Reasoning = *ovr.Reasoning
	}
	if ovr.ThinkingLevelMap != nil {
		e.ThinkingLevelMap = mergeThinkingLevelMaps(e.ThinkingLevelMap, ovr.ThinkingLevelMap)
	}
	if ovr.SamplingParams != nil {
		if e.SamplingParams == nil {
			e.SamplingParams = make(map[string]any, len(ovr.SamplingParams))
		}
		maps.Copy(e.SamplingParams, ovr.SamplingParams)
	}
	e.InputLimits = mergeModelInputLimits(e.InputLimits, ovr.InputLimits)
	if ovr.Input != nil {
		e.Input = append([]string(nil), (*ovr.Input)...)
	}
	if ovr.ContextWindow != nil {
		e.ContextWindow = *ovr.ContextWindow
	}
	if ovr.MaxTokens != nil {
		e.MaxTokens = *ovr.MaxTokens
	}
	if ovr.Cost != nil {
		if ovr.Cost.Input != nil {
			e.InputCost = *ovr.Cost.Input
		}
		if ovr.Cost.Output != nil {
			e.OutputCost = *ovr.Cost.Output
		}
		if ovr.Cost.CacheRead != nil {
			e.CacheReadCost = *ovr.Cost.CacheRead
		}
		if ovr.Cost.CacheWrite != nil {
			e.CacheWriteCost = *ovr.Cost.CacheWrite
		}
		if ovr.Cost.Tiers != nil {
			e.CostTiers = append([]ai.CostTier(nil), (*ovr.Cost.Tiers)...)
		}
	}
	if ovr.PromptCache != nil {
		if e.PromptCache == nil {
			e.PromptCache = make(ai.ModelPromptCache, len(ovr.PromptCache))
		}
		maps.Copy(e.PromptCache, ovr.PromptCache)
	}
	e.Headers = mergeHeadersOrdered(e.Headers, ovr.Headers, ovr.headerEntries, r.providerEnv(e.ProviderID))
	e.Compat = mergeCompat((*providerCompat)(e.Compat), ovr.Compat)
}

func cloneThinkingLevelMap(in ai.ThinkingLevelMap) ai.ThinkingLevelMap {
	if len(in) == 0 {
		return nil
	}
	out := make(ai.ThinkingLevelMap, len(in))
	for k, v := range in {
		if v == nil {
			out[k] = nil
			continue
		}
		value := *v
		out[k] = &value
	}
	return out
}

func mergeThinkingLevelMaps(base, override ai.ThinkingLevelMap) ai.ThinkingLevelMap {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(ai.ThinkingLevelMap, len(base)+len(override))
	maps.Copy(out, cloneThinkingLevelMap(base))
	maps.Copy(out, cloneThinkingLevelMap(override))
	return out
}

func mergeHeadersOrdered(base map[string]string, overrides map[string]*string, ordered []orderedHeaderEntry, env map[string]string) map[string]string {
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overrides))
	maps.Copy(out, base)
	entries := ordered
	if entries == nil {
		keys := slices.Sorted(maps.Keys(overrides))
		entries = make([]orderedHeaderEntry, 0, len(keys))
		for _, name := range keys {
			entries = append(entries, orderedHeaderEntry{Name: name, Value: overrides[name]})
		}
	}
	for _, entry := range entries {
		for existingName := range out {
			if strings.EqualFold(existingName, entry.Name) {
				delete(out, existingName)
			}
		}
		if entry.Value != nil {
			out[entry.Name] = configvalue.Resolve(*entry.Value, env)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeCompat merges two compat structs; b fields override a fields (per-field).
func mergeCompat(a, b *providerCompat) *ai.ModelCompat {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		cp := ai.ModelCompat(*b)
		return &cp
	}
	if b == nil {
		cp := ai.ModelCompat(*a)
		return &cp
	}
	// b overrides a per-field.
	base := ai.ModelCompat(*a)
	ovr := ai.ModelCompat(*b)
	merged := base
	merged.SupportsDeveloperRole = cmp.Or(ovr.SupportsDeveloperRole, merged.SupportsDeveloperRole)
	merged.SupportsReasoningEffort = cmp.Or(ovr.SupportsReasoningEffort, merged.SupportsReasoningEffort)
	merged.SupportsStore = cmp.Or(ovr.SupportsStore, merged.SupportsStore)
	merged.SupportsUsageInStreaming = cmp.Or(ovr.SupportsUsageInStreaming, merged.SupportsUsageInStreaming)
	merged.SupportsFinishReason = cmp.Or(ovr.SupportsFinishReason, merged.SupportsFinishReason)
	merged.MaxTokensField = cmp.Or(ovr.MaxTokensField, merged.MaxTokensField)
	merged.RequiresToolResultName = cmp.Or(ovr.RequiresToolResultName, merged.RequiresToolResultName)
	merged.RequiresAssistantAfterToolResult = cmp.Or(ovr.RequiresAssistantAfterToolResult, merged.RequiresAssistantAfterToolResult)
	merged.RequiresThinkingAsText = cmp.Or(ovr.RequiresThinkingAsText, merged.RequiresThinkingAsText)
	merged.RequiresReasoningContentOnAssistantMessages = cmp.Or(ovr.RequiresReasoningContentOnAssistantMessages, merged.RequiresReasoningContentOnAssistantMessages)
	merged.SupportsOpenAIGrammarTools = cmp.Or(ovr.SupportsOpenAIGrammarTools, merged.SupportsOpenAIGrammarTools)
	merged.SupportsExplicitPromptCacheMode = cmp.Or(ovr.SupportsExplicitPromptCacheMode, merged.SupportsExplicitPromptCacheMode)
	merged.SupportsStrictTools = cmp.Or(ovr.SupportsStrictTools, merged.SupportsStrictTools)
	merged.ThinkingFormat = cmp.Or(ovr.ThinkingFormat, merged.ThinkingFormat)
	merged.CacheControlFormat = cmp.Or(ovr.CacheControlFormat, merged.CacheControlFormat)
	merged.SendSessionAffinityHeaders = cmp.Or(ovr.SendSessionAffinityHeaders, merged.SendSessionAffinityHeaders)
	merged.SupportsStrictMode = cmp.Or(ovr.SupportsStrictMode, merged.SupportsStrictMode)
	merged.OpenRouterRouting = mergeCompatMap(base.OpenRouterRouting, ovr.OpenRouterRouting)
	merged.ChatTemplateKwargs = mergeCompatMap(base.ChatTemplateKwargs, ovr.ChatTemplateKwargs)
	merged.VLLMPriority = cmp.Or(ovr.VLLMPriority, merged.VLLMPriority)
	merged.VercelGatewayRouting = mergeCompatMap(base.VercelGatewayRouting, ovr.VercelGatewayRouting)
	merged.SupportsLongCacheRetention = cmp.Or(ovr.SupportsLongCacheRetention, merged.SupportsLongCacheRetention)
	merged.SendSessionIdHeader = cmp.Or(ovr.SendSessionIdHeader, merged.SendSessionIdHeader)
	merged.SupportsEagerToolInputStreaming = cmp.Or(ovr.SupportsEagerToolInputStreaming, merged.SupportsEagerToolInputStreaming)
	merged.SupportsCacheControlOnTools = cmp.Or(ovr.SupportsCacheControlOnTools, merged.SupportsCacheControlOnTools)
	merged.ForceAdaptiveThinking = cmp.Or(ovr.ForceAdaptiveThinking, merged.ForceAdaptiveThinking)
	merged.AllowEmptySignature = cmp.Or(ovr.AllowEmptySignature, merged.AllowEmptySignature)
	merged.SupportsTemperature = cmp.Or(ovr.SupportsTemperature, merged.SupportsTemperature)
	merged.ZaiToolStream = cmp.Or(ovr.ZaiToolStream, merged.ZaiToolStream)
	merged.SessionAffinityFormat = cmp.Or(ovr.SessionAffinityFormat, merged.SessionAffinityFormat)
	merged.SupportsToolSearch = cmp.Or(ovr.SupportsToolSearch, merged.SupportsToolSearch)
	merged.SupportsMaxOutputTokens = cmp.Or(ovr.SupportsMaxOutputTokens, merged.SupportsMaxOutputTokens)
	merged.ChatTemplateArgs = mergeCompatMap(base.ChatTemplateArgs, ovr.ChatTemplateArgs)
	merged.SupportsThinkingTokenBudget = cmp.Or(ovr.SupportsThinkingTokenBudget, merged.SupportsThinkingTokenBudget)
	merged.SupportsAdditionalTools = cmp.Or(ovr.SupportsAdditionalTools, merged.SupportsAdditionalTools)
	merged.SupportsMidConvoEffort = cmp.Or(ovr.SupportsMidConvoEffort, merged.SupportsMidConvoEffort)
	merged.SupportsMidConvoSystemMessages = cmp.Or(ovr.SupportsMidConvoSystemMessages, merged.SupportsMidConvoSystemMessages)
	merged.SupportsMidConvoToolAdditions = cmp.Or(ovr.SupportsMidConvoToolAdditions, merged.SupportsMidConvoToolAdditions)
	merged.SupportsMidConvoToolChanges = cmp.Or(ovr.SupportsMidConvoToolChanges, merged.SupportsMidConvoToolChanges)
	if ovr.AllowedFallbackModels != nil {
		merged.AllowedFallbackModels = append([]ai.AnthropicAllowedFallbackModel(nil), ovr.AllowedFallbackModels...)
		for i := range merged.AllowedFallbackModels {
			merged.AllowedFallbackModels[i].Cost.Tiers = append([]ai.CostTier(nil), merged.AllowedFallbackModels[i].Cost.Tiers...)
		}
	}
	return &merged
}

func mergeCompatMap(base, override map[string]any) map[string]any {
	if base == nil && override == nil {
		return nil
	}
	merged := maps.Clone(base)
	if merged == nil {
		merged = make(map[string]any, len(override))
	}
	maps.Copy(merged, override)
	return merged
}

// HasAnyKey reports whether the registry has at least one entry for
// the given provider with a non-empty (post-resolve) APIKey, or the
// environment configures its auth.
func (r *ModelRegistry) HasAnyKey(providerID string) bool {
	if prov, ok := r.config.provider(providerID); ok && configvalue.Resolve(prov.APIKey, r.providerEnv(providerID)) != "" {
		return true
	}
	return hasEnvAuth(providerID)
}

// hasEnvAuth reports whether the environment configures request auth for
// providerID. ANTHROPIC_AUTH_TOKEN counts although it resolves no API key.
func hasEnvAuth(providerID string) bool {
	if providerID == "anthropic" && os.Getenv(ai.AnthropicAuthTokenEnv) != "" {
		return true
	}
	return resolveAPIKeyFromEnv(providerID) != ""
}

// resolveAPIKeyFromEnv resolves an API key from the provider's environment
// variables.
func resolveAPIKeyFromEnv(providerID string) string {
	// The Anthropic provider resolves AUTH_TOKEN as a bearer header, not an API key.
	if providerID == "anthropic" && os.Getenv(ai.AnthropicAuthTokenEnv) != "" {
		return ""
	}
	return ai.GetEnvAPIKey(providerID, nil)
}

// stripJSONComments removes // line comments and trailing commas from JSON,
// leaving string literals untouched. This allows models.json to contain
// human-friendly comments and trailing commas.
func stripJSONComments(input string) string {
	// State machine: track whether we're inside a string literal.
	var out []byte
	i := 0
	for i < len(input) {
		c := input[i]
		switch {
		case c == '"':
			// Consume the entire string literal, respecting escape sequences.
			start := i
			i++
			for i < len(input) {
				ch := input[i]
				if ch == '\\' {
					i += 2 // skip escaped character
					continue
				}
				i++
				if ch == '"' {
					break
				}
			}
			out = append(out, input[start:i]...)
		case c == '/' && i+1 < len(input) && input[i+1] == '/':
			// Skip to end of line.
			for i < len(input) && input[i] != '\n' {
				i++
			}
		case c == ',':
			// Look ahead (skipping whitespace) to check for trailing comma
			// before } or ].
			j := i + 1
			for j < len(input) && (input[j] == ' ' || input[j] == '\t' || input[j] == '\r' || input[j] == '\n') {
				j++
			}
			if j < len(input) && (input[j] == '}' || input[j] == ']') {
				i++ // drop the trailing comma
			} else {
				out = append(out, c)
				i++
			}
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}
