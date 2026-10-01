package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// BuildModel constructs an *ai.Model from a "provider/model" spec, using the
// given Services for auth and registry lookup. It is the one provider factory:
// startup, /model, routing, RPC and SDK consumers all build models here. A bare
// "<model>" with no slash is interpreted as "openai/<model>".
//
// The catalog entry for the exact provider/model supplies capabilities and the
// endpoint; a models.json definition comes next. A model id unknown under a
// catalogued provider borrows that provider's default model (never another
// provider's same-named model, so credentials stay with their provider's
// host); BuildModelWithWarning reports that case.
//
//	svcs, _ := coding.NewServices(...)
//	model, _ := coding.BuildModel("github-copilot/gpt-4o-mini", svcs)
//	sess, _ := coding.StartSession(svcs, coding.SessionStartOptions{Model: model})
func BuildModel(spec string, svcs *Services) (*ai.Model, error) {
	model, _, err := BuildModelWithWarning(spec, svcs)
	return model, err
}

// BuildModelWithWarning is BuildModel plus a warning when the model id is
// unknown under its provider and uses the provider's default capabilities.
func BuildModelWithWarning(spec string, svcs *Services) (*ai.Model, string, error) {
	if svcs == nil {
		return nil, "", fmt.Errorf("coding: BuildModel: Services is required")
	}
	providerID, modelID, ok := strings.Cut(spec, "/")
	if !ok {
		providerID, modelID = "openai", spec
	}

	registry := svcs.Registry()
	var entry icodingagent.ModelEntry
	var warning string
	if generated, ok := ai.LookupModelExact(providerID + "/" + modelID); ok {
		entry = registry.ResolveCatalogModel(providerID, modelID, generated)
	} else if inferred, ok := ai.InferModel(providerID + "/" + modelID); ok && !registry.HasModelDefinition(providerID, modelID) {
		entry = registry.ResolveCatalogModel(providerID, modelID, inferred)
		warning = fmt.Sprintf("Model %q isn't in %s's list or models.dev yet; using its closest relative's settings.", modelID, providerID)
	} else if base, ok := providerDefaultModel(providerID); ok && !registry.HasModelDefinition(providerID, modelID) {
		entry = registry.ResolveCatalogModel(providerID, modelID, base)
		entry.DisplayName = modelID
		warning = fmt.Sprintf("Model %q not found for provider %q. Using custom model id.", modelID, providerID)
	} else {
		entry, _ = registry.Resolve(providerID, modelID)
		entry.ProviderID, entry.ModelID = providerID, modelID
	}

	provider, err := buildProviderForEntry(providerID, modelID, ai.API(entry.API), entry, svcs)
	if err != nil {
		return nil, "", err
	}
	provider = newProviderAttributionProvider(provider, providerID, entry.BaseURL, func() bool {
		return svcs.settings.GetEnableAttributionHeaders()
	}, entry.Headers)
	if accepts, known := ai.AcceptsImages(entry.Input); known && !accepts && providerID != "test-faux" {
		provider.(*providerAttributionProvider).textOnly = true
	}

	if providerID == "test-faux" {
		entry.Input = []string{"text", "image"}
		entry.ContextWindow = ai.TestFauxContextWindow
		entry.MaxTokens = ai.TestFauxMaxTokens
		entry.DisplayName = "Test Faux"
		entry.API = "test-faux"
		entry.BaseURL = "http://localhost:0"
	}
	return modelFromEntry(entry, provider), warning, nil
}

// providerDefaultModels lists each provider's default model, in the order
// startup walks them when no model is configured.
var providerDefaultModels = []ProviderModel{
	{"amazon-bedrock", "us.anthropic.claude-opus-4-6-v1"},
	{"ant-ling", "Ring-2.6-1T"},
	{"anthropic", "claude-opus-4-8"},
	{"openai", "gpt-5.5"},
	{"azure-openai-responses", "gpt-5.4"},
	{"openai-codex", "gpt-5.5"},
	{"nvidia", "nvidia/nemotron-3-super-120b-a12b"},
	{"deepseek", "deepseek-v4-pro"},
	{"google", "gemini-3.1-pro-preview"},
	{"google-vertex", "gemini-3.1-pro-preview"},
	{"github-copilot", "gpt-5.4"},
	{"openrouter", "moonshotai/kimi-k2.6"},
	{"vercel-ai-gateway", "zai/glm-5.1"},
	{"xai", "grok-4.7"},
	{"groq", "openai/gpt-oss-120b"},
	{"cerebras", "gpt-oss-120b"},
	{"zai", "glm-5.3"},
	{"zai-coding-cn", "glm-5.3"},
	{"mistral", "devstral-medium-latest"},
	{"minimax", "MiniMax-M2.7"},
	{"minimax-cn", "MiniMax-M2.7"},
	{"moonshotai", "kimi-k2.6"},
	{"moonshotai-cn", "kimi-k2.6"},
	{"huggingface", "moonshotai/Kimi-K2.6"},
	{"fireworks", "accounts/fireworks/models/kimi-k2p6"},
	{"together", "moonshotai/Kimi-K2.6"},
	{"baseten", "zai-org/GLM-5.2"},
	{"opencode", "kimi-k2.6"},
	{"opencode-go", "kimi-k2.6"},
	{"kimi-coding", "kimi-for-coding"},
	{"meta", "muse-spark-1.3"},
	{"cloudflare-workers-ai", "@cf/moonshotai/kimi-k2.6"},
	{"cloudflare-ai-gateway", "workers-ai/@cf/moonshotai/kimi-k2.6"},
	{"qwen-token-plan", "qwen3.7-max"},
	{"qwen-token-plan-cn", "qwen3.7-max"},
	{"qwen-token-plan-individual", "qwen3.8-max"},
	{"xiaomi", "mimo-v2.5-pro"},
	{"xiaomi-token-plan-cn", "mimo-v2.5-pro"},
	{"xiaomi-token-plan-ams", "mimo-v2.5-pro"},
	{"xiaomi-token-plan-sgp", "mimo-v2.5-pro"},
}

// ProviderModel names one model under one provider.
type ProviderModel struct{ Provider, ModelID string }

// DefaultProviderModels returns each provider's default model, in startup
// preference order.
func DefaultProviderModels() []ProviderModel { return slices.Clone(providerDefaultModels) }

// providerDefaultModel returns the catalog model an unknown id under
// providerID borrows: the provider's default model when catalogued, else its
// first catalogued model.
func providerDefaultModel(providerID string) (*ai.KnownModel, bool) {
	for _, entry := range providerDefaultModels {
		if entry.Provider == providerID {
			if m, ok := ai.LookupModelExact(providerID + "/" + entry.ModelID); ok {
				return m, true
			}
		}
	}
	if models := ai.ListModels(providerID); len(models) > 0 {
		return &models[0], true
	}
	return nil, false
}

func modelFromEntry(entry icodingagent.ModelEntry, provider ai.Provider) *ai.Model {
	displayName := entry.DisplayName
	if displayName == "" {
		displayName = entry.ModelID
	}
	input := slices.Clone(entry.Input)
	return &ai.Model{
		ID:          entry.ModelID,
		DisplayName: displayName,
		Provider:    provider,
		Capabilities: ai.ModelCapabilities{
			MaxThinking:         thinkingMaxLevelForEntry(entry),
			SupportsImages:      slices.Contains(entry.Input, "image"),
			SupportsToolUse:     true,
			ContextWindow:       entry.ContextWindow,
			MaxOutputTokens:     entry.MaxTokens,
			InputCostPer1M:      entry.InputCost,
			OutputCostPer1M:     entry.OutputCost,
			CacheReadCostPer1M:  entry.CacheReadCost,
			CacheWriteCostPer1M: entry.CacheWriteCost,
			CostTiers:           append([]ai.CostTier(nil), entry.CostTiers...),
		},
		Input:            input,
		InputLimits:      entry.InputLimits.Clone(),
		ThinkingLevelMap: cloneThinkingLevelMap(entry.ThinkingLevelMap),
		SamplingParams:   maps.Clone(entry.SamplingParams),
		PromptCache:      maps.Clone(entry.PromptCache),
		ProviderMeta: ai.ProviderMetadata{
			ProviderID: entry.ProviderID,
			API:        ai.API(entry.API),
			BaseURL:    entry.BaseURL,
			Headers:    cloneStringMap(entry.ModelHeaders),
			Compat:     cloneCompat(entry.Compat),
			Reasoning:  entry.Reasoning,
		},
	}
}

func thinkingMaxLevelForEntry(entry icodingagent.ModelEntry) ai.ThinkingLevel {
	if !entry.Reasoning {
		return ""
	}
	model := &ai.Model{
		Capabilities:     ai.ModelCapabilities{MaxThinking: ai.ThinkingHigh},
		ThinkingLevelMap: entry.ThinkingLevelMap,
	}
	levels := ai.GetSupportedThinkingLevels(model)
	return levels[len(levels)-1]
}

func buildProviderForEntry(providerID, modelID string, apiKind ai.API, entry icodingagent.ModelEntry, svcs *Services) (ai.Provider, error) {
	apiKey := entry.APIKey
	baseURL := entry.BaseURL
	extraHeaders := cloneStringMap(entry.Headers)

	if providerID == "test-faux" {
		if os.Getenv("WOPR_TEST_FAUX") != "1" {
			return nil, fmt.Errorf("coding: BuildModel: test-faux is disabled")
		}
		return svcs.testFauxProvider(), nil
	}

	if providerID == "github-copilot" {
		// Re-open auth from disk so this provider has its own handle
		// (the Services-owned handle is shared and may be in-use).
		auth, err := ai.NewAuthStorage(filepath.Join(svcs.AgentDir(), "auth.json"))
		if err != nil {
			return nil, fmt.Errorf("coding: BuildModel: github-copilot auth: %w", err)
		}
		return ai.NewCopilotProvider(ai.CopilotProviderConfig{
			Auth:      auth,
			Model:     modelID,
			API:       apiKind,
			Reasoning: entry.Reasoning,
			// EnvToken carries the env-resolved API key (entry.APIKey is set
			// from COPILOT_GITHUB_TOKEN/GH_TOKEN/GITHUB_TOKEN for github-copilot)
			// so the provider can fall back to it when auth.json has no OAuth
			// credential.
			EnvToken: entry.APIKey,
			RuntimeToken: func() (string, bool) {
				return svcs.Registry().RuntimeAPIKey(providerID)
			},
		})
	}

	// A runtime or stored credential owns the provider ahead of the static
	// registry or env key resolved above. Providers that accept a per-request
	// key callback resolve it there so an OAuth refresh follows the request
	// context; the others resolve it here, once per BuildModel.
	resolveAPIKey := requestAPIKey(svcs, providerID, apiKey)
	if !acceptsRequestAPIKey(apiKind) {
		var err error
		if apiKey, err = resolveAPIKey(context.Background()); err != nil {
			return nil, fmt.Errorf("coding: BuildModel: %s: %w", providerID, err)
		}
	}
	if providerID == "ollama" && baseURL == "" {
		baseURL = firstNonEmpty(os.Getenv("OLLAMA_HOST"), "http://localhost:11434/v1")
	}
	switch apiKind {
	case ai.APIOpenAICompletions:
		return ai.NewOpenAIProvider(ai.OpenAIConfig{
			BaseURL:        baseURL,
			APIKey:         apiKey,
			GetAPIKey:      resolveAPIKey,
			Model:          modelID,
			ProviderID:     providerID,
			ExtraHeaders:   extraHeaders,
			SamplingParams: maps.Clone(entry.SamplingParams),
			Env:            ai.ProviderEnv(maps.Clone(entry.Env)),
			Compat:         cloneCompat(entry.Compat),
			Insecure:       entry.Insecure,
			Input:          slices.Clone(entry.Input),
		}), nil
	case ai.APIOpenAIResponses:
		return ai.NewOpenAIResponsesProvider(ai.OpenAIResponsesConfig{
			BaseURL:        baseURL,
			APIKey:         apiKey,
			GetAPIKey:      resolveAPIKey,
			Model:          modelID,
			ProviderID:     providerID,
			ExtraHeaders:   extraHeaders,
			SamplingParams: maps.Clone(entry.SamplingParams),
			Env:            ai.ProviderEnv(maps.Clone(entry.Env)),
			Compat:         cloneCompat(entry.Compat),
			IsReasoning:    entry.Reasoning,
			Input:          slices.Clone(entry.Input),
		}), nil
	case ai.APIOpenAICodexResponses:
		return ai.NewOpenAICodexResponsesProvider(ai.OpenAICodexResponsesConfig{
			Compat:     cloneCompat(entry.Compat),
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Model:      modelID,
			ProviderID: providerID,
		}), nil
	case ai.APIAzureOpenAIResponses:
		return ai.NewAzureOpenAIResponsesProvider(ai.AzureOpenAIResponsesConfig{
			Compat:         cloneCompat(entry.Compat),
			BaseURL:        baseURL,
			APIKey:         apiKey,
			Model:          modelID,
			ProviderID:     providerID,
			ExtraHeaders:   extraHeaders,
			SamplingParams: maps.Clone(entry.SamplingParams),
			Env:            ai.ProviderEnv(maps.Clone(entry.Env)),
		}), nil
	case ai.APIAnthropicMessages:
		config := ai.AnthropicConfig{
			BaseURL:      baseURL,
			APIKey:       apiKey,
			Model:        modelID,
			ProviderID:   providerID,
			ExtraHeaders: extraHeaders,
			Compat:       cloneCompat(entry.Compat),
			GetAPIKey:    resolveAPIKey,
			Env:          ai.ProviderEnv(maps.Clone(entry.Env)),
		}
		return ai.NewAnthropicProvider(config), nil
	case ai.APIGoogleGenerativeAI:
		return ai.NewGoogleProvider(ai.GoogleConfig{
			BaseURL:      baseURL,
			APIKey:       apiKey,
			Model:        modelID,
			ProviderID:   providerID,
			APIVersion:   googleAPIVersionForBaseURL(baseURL),
			ExtraHeaders: extraHeaders,
			Input:        slices.Clone(entry.Input),
		}), nil
	case ai.APIGoogleVertex:
		return ai.NewGoogleVertexProvider(ai.GoogleVertexConfig{
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Model:      modelID,
			ProviderID: providerID,
			Headers:    extraHeaders,
		}), nil
	case ai.APIBedrockConverseStream:
		// Amazon Bedrock uses AWS native auth (SigV4 via the default
		// credentials chain, or AWS_BEARER_TOKEN_BEDROCK). No API key
		// from env or registry is required at construction time; the
		// AWS SDK resolves credentials at request time.
		return ai.NewBedrockProvider(modelID, entry.DisplayName, baseURL), nil
	case ai.APIMistralConversations:
		return ai.NewMistralProvider(ai.MistralConfig{
			BaseURL:      baseURL,
			APIKey:       apiKey,
			Model:        modelID,
			ProviderID:   providerID,
			ExtraHeaders: extraHeaders,
			Reasoning:    entry.Reasoning,
		}), nil
	default:
		return ai.NewOpenAIProvider(ai.OpenAIConfig{
			BaseURL:        baseURL,
			APIKey:         apiKey,
			GetAPIKey:      resolveAPIKey,
			Model:          modelID,
			ProviderID:     providerID,
			ExtraHeaders:   extraHeaders,
			SamplingParams: maps.Clone(entry.SamplingParams),
			Env:            ai.ProviderEnv(maps.Clone(entry.Env)),
			Compat:         cloneCompat(entry.Compat),
			Insecure:       entry.Insecure,
			Input:          slices.Clone(entry.Input),
		}), nil
	}
}

// acceptsRequestAPIKey reports whether BuildModel's provider for apiKind
// resolves its key through a per-request callback.
func acceptsRequestAPIKey(apiKind ai.API) bool {
	switch apiKind {
	case ai.APIOpenAICodexResponses, ai.APIAzureOpenAIResponses, ai.APIGoogleGenerativeAI, ai.APIGoogleVertex, ai.APIBedrockConverseStream, ai.APIMistralConversations:
		return false
	default:
		return true
	}
}

// requestAPIKey resolves a provider's request key: a non-persistent runtime
// key (--api-key) first, then a stored auth.json credential, then fallback,
// the configured or environment key.
func requestAPIKey(svcs *Services, providerID, fallback string) func(context.Context) (string, error) {
	stored := storedAPIKey(filepath.Join(svcs.AgentDir(), "auth.json"), providerID, fallback)
	registry := svcs.Registry().ModelRegistry
	return func(ctx context.Context) (string, error) {
		if key, ok := registry.RuntimeAPIKey(providerID); ok {
			return key, nil
		}
		return stored(ctx)
	}
}

// storedAPIKey resolves request auth per request: a stored OAuth login
// (refreshed when expiring) or stored api_key credential owns the provider,
// ahead of the configured or
// environment key fallback. An unopenable auth store yields the fallback; a
// failed read or refresh is returned to the request.
func storedAPIKey(authPath, providerID, fallback string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		auth, err := ai.NewAuthStorage(authPath)
		if err != nil {
			return fallback, nil
		}
		key, ok, err := ai.ResolveStoredAPIKeyFromStorage(ctx, auth, providerID)
		if err != nil || ok {
			return key, err
		}
		return fallback, nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func googleAPIVersionForBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, "/v1") || strings.HasSuffix(trimmed, "/v1beta") {
		return ""
	}
	return "v1beta"
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func cloneCompat(in *ai.ModelCompat) *ai.ModelCompat {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out ai.ModelCompat
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return &out
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

// Stream delegates to the Services-owned ModelRuntime.
func (registry *ModelRegistry) Stream(ctx context.Context, model *ai.Model, request ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
	return registry.runtime.Stream(ctx, model, request, options)
}

// StreamSimple delegates to the same runtime preparation and streaming path.
func (registry *ModelRegistry) StreamSimple(ctx context.Context, model *ai.Model, request ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
	return registry.runtime.StreamSimple(ctx, model, request, options)
}

// Complete delegates to ModelRuntime.Complete and returns its terminal pointer.
func (registry *ModelRegistry) Complete(ctx context.Context, model *ai.Model, request ai.Context, options ai.StreamOptions) *ai.AssistantMessage {
	return registry.runtime.Complete(ctx, model, request, options)
}

const (
	openRouterHost        = "openrouter.ai"
	nvidiaNIMHost         = "integrate.api.nvidia.com"
	cloudflareAPIHost     = "api.cloudflare.com"
	cloudflareGatewayHost = "gateway.ai.cloudflare.com"
	openCodeHost          = "opencode.ai"
)

// mergeProviderAttributionHeaders adds provider attribution headers: the
// OpenCode session pair is unconditional, the OpenRouter/NVIDIA/Cloudflare
// headers are gated on attributionEnabled. Values carry wopr branding.
func mergeProviderAttributionHeaders(providerID, baseURL string, attributionEnabled bool, sessionID string, headerSources ...ai.ProviderHeaders) ai.ProviderHeaders {
	headers := make(ai.ProviderHeaders)
	set := func(name, value string) { headers[name] = new(value) }
	if sessionID != "" && (providerID == "opencode" || providerID == "opencode-go" || matchesProviderHost(baseURL, openCodeHost)) {
		set("x-opencode-session", sessionID)
		set("x-opencode-client", "wopr")
	}

	if attributionEnabled {
		switch {
		// OpenRouter matches the base URL by substring, not host.
		case providerID == "openrouter" || strings.Contains(baseURL, openRouterHost):
			set("HTTP-Referer", "https://github.com/alexrudloff/wopr")
			set("X-OpenRouter-Title", "WOPR")
			set("X-OpenRouter-Categories", "cli-agent")
		case providerID == "nvidia" || matchesProviderHost(baseURL, nvidiaNIMHost):
			set("X-BILLING-INVOKE-ORIGIN", "WOPR")
		case providerID == "cloudflare-workers-ai" || providerID == "cloudflare-ai-gateway" || matchesProviderHost(baseURL, cloudflareAPIHost) || matchesProviderHost(baseURL, cloudflareGatewayHost):
			set("User-Agent", "wopr")
		}
	}

	for _, source := range headerSources {
		for name, value := range source {
			for existing := range headers {
				if strings.EqualFold(existing, name) {
					delete(headers, existing)
				}
			}
			headers[name] = value
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func matchesProviderHost(baseURL, expectedHost string) bool {
	parsed, err := url.Parse(baseURL)
	// WHATWG URL.hostname normalizes DNS host case before comparison.
	return err == nil && strings.ToLower(parsed.Hostname()) == expectedHost
}

type providerAttributionProvider struct {
	ai.Provider
	providerID         string
	baseURL            string
	attributionEnabled func() bool
	configured         map[string]string
	// textOnly marks a model whose input types leave out images: every
	// image in the transcript becomes ai.ImageOmittedText before any client
	// sees it.
	textOnly bool
}

func newProviderAttributionProvider(provider ai.Provider, providerID, baseURL string, attributionEnabled func() bool, configured map[string]string) ai.Provider {
	return &providerAttributionProvider{
		Provider:           provider,
		providerID:         providerID,
		baseURL:            baseURL,
		attributionEnabled: attributionEnabled,
		configured:         maps.Clone(configured),
	}
}

func (provider *providerAttributionProvider) Stream(ctx context.Context, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	if provider.textOnly {
		transcript = transcript.WithoutImages()
	}
	attributionEnabled := false
	if provider.attributionEnabled != nil {
		attributionEnabled = provider.attributionEnabled()
	}
	options.Headers = mergeProviderAttributionHeaders(
		provider.providerID,
		provider.baseURL,
		attributionEnabled,
		options.SessionID,
		ai.ProviderHeadersFromStrings(provider.configured),
		options.Headers,
	)
	// Merge the attribution headers, then run the request's header transform
	// (before_provider_headers) on the result.
	if options.TransformHeaders != nil {
		if options.Headers == nil {
			options.Headers = ai.ProviderHeaders{}
		}
		transformed, err := options.TransformHeaders(ctx, options.Headers)
		if err != nil {
			return nil, err
		}
		options.Headers = transformed
		options.TransformHeaders = nil
	}
	return provider.Provider.Stream(ctx, transcript, options)
}
