package ai

import (
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Per-provider and per-family rules: how wopr talks to each provider's
// models. Nothing here names a single model; rules match families
// (claude-opus ≥ 5, gpt ≥ 5.4) so a model released tomorrow gets them too.

type providerRule struct {
	id string
	// modelsDev is the provider's key in models.dev ("" when models.dev
	// doesn't list it).
	modelsDev string
	api       API
	baseURL   string
	headers   map[string]string
	// route picks a model's API and base URL when a provider serves
	// several (gateways); nil means api and baseURL.
	route func(id string, md *modelsDevModel) (API, string)
	// mapID turns a models.dev id into the id the provider takes; "" skips
	// the model.
	mapID func(mdID string) string
	// include filters models.dev's list (used before a live list exists).
	include     func(id string) bool
	compat      func(id string, api API, md *modelsDevModel) *ModelCompat
	promptCache ModelPromptCache
	// Request and image limits.
	maxRequestBytes  int
	imagesPerMessage int
	imagesPerRequest func(contextWindow int) int
	// minimalAsLow sends "low" for the minimal level when the model has
	// no minimal effort (Codex).
	minimalAsLow bool
	// echoReasoning marks providers that need DeepSeek-style models'
	// reasoning sent back on assistant messages; deepseekFormat also sends
	// their thinking in DeepSeek's format.
	echoReasoning, deepseekFormat bool
	// listAPI and listBase are where the provider's model list lives when
	// that isn't api and baseURL (an OpenAI-compatible list beside an
	// Anthropic-compatible API).
	listAPI  API
	listBase string
	// tierWindow stops the context window at the first long-context price
	// tier, where every request above it bills at the higher rate (OpenAI's
	// 272K).
	tierWindow bool
}

var defaultImageResize = ModelImageResizeOptions{MaxWidth: 2000, MaxHeight: 2000, MaxBytes: 4718592, JPEGQuality: 80}

func fixed(n int) func(int) int { return func(int) int { return n } }

func staticCompat(c ModelCompat) func(string, API, *modelsDevModel) *ModelCompat {
	return func(string, API, *modelsDevModel) *ModelCompat { out := c; return cloneCompat(&out) }
}

// Compat shapes shared by several providers.
var (
	compatChatPlain = ModelCompat{SupportsDeveloperRole: new(false), SupportsStore: new(false)}
	compatMaxTokens = ModelCompat{MaxTokensField: "max_tokens", SupportsDeveloperRole: new(false), SupportsStore: new(false), SupportsStrictMode: new(true)}
)

var providerRules = []providerRule{
	{
		id: "amazon-bedrock", modelsDev: "amazon-bedrock", api: APIBedrockConverseStream,
		baseURL: "https://bedrock-runtime.us-east-1.amazonaws.com",
		route: func(id string, _ *modelsDevModel) (API, string) {
			if strings.HasPrefix(id, "eu.") {
				return APIBedrockConverseStream, "https://bedrock-runtime.eu-central-1.amazonaws.com"
			}
			return APIBedrockConverseStream, "https://bedrock-runtime.us-east-1.amazonaws.com"
		},
		// Bedrock's strict tool schemas are its structured-output models.
		compat: func(_ string, _ API, md *modelsDevModel) *ModelCompat {
			if md != nil && md.StructuredOutput != nil && *md.StructuredOutput {
				return &ModelCompat{SupportsStrictMode: new(true)}
			}
			return nil
		},
		imagesPerMessage: 20,
	},
	{
		id: "ant-ling", modelsDev: "bailing", api: APIOpenAICompletions, baseURL: "https://api.ant-ling.com/v1",
		compat: staticCompat(ModelCompat{MaxTokensField: "max_tokens", SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsReasoningEffort: new(false), SupportsStore: new(false), SupportsStrictMode: new(true), ThinkingFormat: "ant-ling"}),
	},
	{
		id: "anthropic", modelsDev: "anthropic", api: APIAnthropicMessages, baseURL: "https://api.anthropic.com",
		compat:          staticCompat(ModelCompat{SupportsStrictTools: new(true)}),
		promptCache:     ModelPromptCache{"long": 3600, "short": 300},
		maxRequestBytes: 32 << 20,
		// Anthropic takes 600 images in a request on its 1M-context models
		// and 100 on the rest.
		imagesPerRequest: func(contextWindow int) int {
			if contextWindow >= 1_000_000 {
				return 600
			}
			return 100
		},
	},
	{
		id: "azure-openai-responses", modelsDev: "azure", api: APIAzureOpenAIResponses,
		compat: func(id string, _ API, _ *modelsDevModel) *ModelCompat {
			if gptAtLeast(id, 5, 0) {
				return &ModelCompat{SupportsOpenAIGrammarTools: new(true)}
			}
			return nil
		},
	},
	{
		id: "baseten", modelsDev: "baseten", api: APIOpenAICompletions, baseURL: "https://inference.baseten.co/v1",
		compat: func(id string, _ API, md *modelsDevModel) *ModelCompat {
			c := ModelCompat{MaxTokensField: "max_tokens", SendSessionAffinityHeaders: new(true), SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsStore: new(false), SupportsStrictMode: new(true), SupportsUsageInStreaming: new(true)}
			if basetenTemplateThinking.MatchString(strings.ToLower(id)) {
				c.ChatTemplateArgs = map[string]any{"enable_thinking": map[string]any{"$var": "thinking.enabled"}}
				c.ThinkingFormat = "baseten"
				c.SupportsReasoningEffort = new(md != nil && len(md.efforts()) > 0)
			} else {
				c.ThinkingFormat = "openai"
				c.SupportsReasoningEffort = new(true)
			}
			return &c
		},
	},
	{
		id: "cerebras", modelsDev: "cerebras", api: APIOpenAICompletions, baseURL: "https://api.cerebras.ai/v1",
		compat: staticCompat(compatChatPlain),
	},
	{
		id: "cloudflare-ai-gateway", modelsDev: "cloudflare-ai-gateway", api: APIOpenAICompletions, echoReasoning: true, deepseekFormat: true,
		baseURL: cloudflareGatewayBase + "/compat",
		// The gateway serves Anthropic and OpenAI models over their own
		// APIs (ids without the vendor prefix) and Workers AI models over
		// its OpenAI-compatible endpoint.
		mapID: func(mdID string) string {
			switch {
			case strings.HasPrefix(mdID, "anthropic/"), strings.HasPrefix(mdID, "openai/"):
				_, id, _ := strings.Cut(mdID, "/")
				return id
			case strings.HasPrefix(mdID, "workers-ai/"):
				return mdID
			}
			return ""
		},
		route: func(id string, _ *modelsDevModel) (API, string) {
			switch {
			case strings.HasPrefix(id, "claude-"):
				return APIAnthropicMessages, cloudflareGatewayBase + "/anthropic"
			case strings.HasPrefix(id, "workers-ai/"):
				return APIOpenAICompletions, cloudflareGatewayBase + "/compat"
			}
			return APIOpenAIResponses, cloudflareGatewayBase + "/openai"
		},
		compat: func(id string, api API, _ *modelsDevModel) *ModelCompat {
			switch api {
			case APIAnthropicMessages:
				return &ModelCompat{SendSessionAffinityHeaders: new(true)}
			case APIOpenAIResponses:
				c := &ModelCompat{SupportsStrictMode: new(true)}
				if gptAtLeast(id, 5, 0) {
					c.SupportsOpenAIGrammarTools = new(true)
				}
				return c
			}
			return &ModelCompat{MaxTokensField: "max_tokens", SendSessionAffinityHeaders: new(true), SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsReasoningEffort: new(false), SupportsStore: new(false)}
		},
	},
	{
		id: "cloudflare-workers-ai", modelsDev: "cloudflare-workers-ai", api: APIOpenAICompletions, echoReasoning: true, deepseekFormat: true,
		baseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1",
		compat:  staticCompat(ModelCompat{SendSessionAffinityHeaders: new(true), SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsStore: new(false), SupportsStrictMode: new(true)}),
	},
	{
		id: "deepseek", modelsDev: "deepseek", api: APIOpenAICompletions, baseURL: "https://api.deepseek.com",
		compat: staticCompat(ModelCompat{MaxTokensField: "max_tokens", RequiresReasoningContentOnAssistantMessages: new(true), SupportsDeveloperRole: new(false), SupportsStore: new(false), SupportsStrictMode: new(true), ThinkingFormat: "deepseek"}),
	},
	{
		id: "fireworks", modelsDev: "fireworks-ai", api: APIAnthropicMessages, baseURL: "https://api.fireworks.ai/inference",
		listAPI: APIOpenAICompletions, listBase: "https://api.fireworks.ai/inference/v1",
		// Fireworks serves most models over its Anthropic-compatible API;
		// GLM and Kimi K3 only over its OpenAI-compatible one.
		route: func(id string, _ *modelsDevModel) (API, string) {
			if fireworksCompletions.MatchString(strings.ToLower(id)) {
				return APIOpenAICompletions, "https://api.fireworks.ai/inference/v1"
			}
			return APIAnthropicMessages, "https://api.fireworks.ai/inference"
		},
		compat: func(_ string, api API, md *modelsDevModel) *ModelCompat {
			if api == APIOpenAICompletions {
				return &ModelCompat{SendSessionAffinityHeaders: new(true), SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsStore: new(false), SupportsStrictMode: new(true)}
			}
			c := &ModelCompat{AllowEmptySignature: new(true), SendSessionAffinityHeaders: new(true), SupportsCacheControlOnTools: new(false), SupportsEagerToolInputStreaming: new(false), SupportsLongCacheRetention: new(false)}
			if md != nil && len(md.efforts()) > 0 {
				c.ForceAdaptiveThinking = new(true)
			}
			return c
		},
	},
	{
		id: "github-copilot", modelsDev: "github-copilot", api: APIOpenAICompletions,
		baseURL: "https://api.individual.githubcopilot.com",
		headers: map[string]string{"Copilot-Integration-Id": "vscode-chat", "Editor-Plugin-Version": "copilot-chat/0.35.0", "Editor-Version": "vscode/1.107.0", "User-Agent": "GitHubCopilotChat/0.35.0"},
		route: func(id string, _ *modelsDevModel) (API, string) {
			const base = "https://api.individual.githubcopilot.com"
			switch {
			case strings.HasPrefix(id, "claude-"):
				return APIAnthropicMessages, base
			case strings.HasPrefix(id, "gpt-"), strings.HasPrefix(id, "grok-"), strings.HasPrefix(id, "mai-"):
				return APIOpenAIResponses, base
			}
			return APIOpenAICompletions, base
		},
		compat: func(id string, api API, _ *modelsDevModel) *ModelCompat {
			switch api {
			case APIAnthropicMessages:
				return nil
			case APIOpenAIResponses:
				return gptResponsesCompat(id, false, 4)
			}
			return &ModelCompat{SupportsDeveloperRole: new(false), SupportsReasoningEffort: new(false), SupportsStore: new(false), SupportsStrictMode: new(true)}
		},
	},
	{
		id: "google", modelsDev: "google", api: APIGoogleGenerativeAI, baseURL: "https://generativelanguage.googleapis.com/v1beta",
		maxRequestBytes: 20 << 20, imagesPerRequest: fixed(3600),
	},
	{
		id: "google-vertex", modelsDev: "google-vertex", api: APIGoogleVertex, baseURL: "https://{location}-aiplatform.googleapis.com",
		include: func(id string) bool { return strings.HasPrefix(id, "gemini-") },
	},
	{
		id: "groq", modelsDev: "groq", api: APIOpenAICompletions, baseURL: "https://api.groq.com/openai/v1",
		compat: staticCompat(ModelCompat{SupportsStrictMode: new(true)}),
	},
	{
		id: "huggingface", modelsDev: "huggingface", api: APIOpenAICompletions, baseURL: "https://router.huggingface.co/v1",
		compat: staticCompat(ModelCompat{SupportsDeveloperRole: new(false), SupportsStrictMode: new(true)}),
	},
	{
		id: "kimi-coding", modelsDev: "kimi-code-plan-global", api: APIAnthropicMessages, baseURL: "https://api.kimi.com/coding",
		compat: staticCompat(ModelCompat{ForceAdaptiveThinking: new(true)}),
	},
	{
		id: "meta", modelsDev: "meta", api: APIOpenAIResponses, baseURL: "https://api.meta.ai/v1",
	},
	{
		id: "minimax", modelsDev: "minimax", api: APIAnthropicMessages, baseURL: "https://api.minimax.io/anthropic",
	},
	{
		id: "minimax-cn", modelsDev: "minimax-cn", api: APIAnthropicMessages, baseURL: "https://api.minimaxi.com/anthropic",
	},
	{
		id: "mistral", modelsDev: "mistral", api: APIMistralConversations, baseURL: "https://api.mistral.ai",
	},
	moonshotRule("moonshotai", "https://api.moonshot.ai/v1"),
	moonshotRule("moonshotai-cn", "https://api.moonshot.cn/v1"),
	{
		id: "nvidia", modelsDev: "nvidia", api: APIOpenAICompletions, baseURL: "https://integrate.api.nvidia.com/v1",
		headers: map[string]string{"NVCF-POLL-SECONDS": "3600"},
		compat:  staticCompat(ModelCompat{MaxTokensField: "max_tokens", SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsReasoningEffort: new(false), SupportsStore: new(false), SupportsStrictMode: new(false)}),
	},
	{
		id: "openai", modelsDev: "openai", api: APIOpenAIResponses, baseURL: "https://api.openai.com/v1",
		compat: func(id string, _ API, _ *modelsDevModel) *ModelCompat {
			c := gptResponsesCompat(id, true, 4)
			c.SupportsStrictMode = new(true)
			if gptAtLeast(id, 5, 6) {
				c.SupportsExplicitPromptCacheMode = new(true)
			}
			return c
		},
		maxRequestBytes: 512 << 20, imagesPerRequest: fixed(1500), tierWindow: true,
	},
	{
		id: "openai-codex", modelsDev: "openai", api: APIOpenAICodexResponses, baseURL: "https://chatgpt.com/backend-api",
		// The subscription serves the current GPT-5.3+ coding models; its
		// own list (read at setup and in the background) is the truth.
		include: func(id string) bool {
			return gptAtLeast(id, 5, 3) && !codexExcluded.MatchString(id)
		},
		compat:       func(id string, _ API, _ *modelsDevModel) *ModelCompat { return gptResponsesCompat(id, true, 6) },
		minimalAsLow: true, tierWindow: true,
	},
	opencodeRule("opencode", "https://opencode.ai/zen"),
	opencodeRule("opencode-go", "https://opencode.ai/zen/go"),
	{
		id: "openrouter", modelsDev: "openrouter", api: APIOpenAICompletions, baseURL: "https://openrouter.ai/api/v1", echoReasoning: true,
		route: func(id string, _ *modelsDevModel) (API, string) {
			if strings.HasPrefix(id, "anthropic/") {
				return APIAnthropicMessages, "https://openrouter.ai/api"
			}
			return APIOpenAICompletions, "https://openrouter.ai/api/v1"
		},
		compat: func(id string, api API, _ *modelsDevModel) *ModelCompat {
			if api == APIAnthropicMessages {
				return nil
			}
			c := &ModelCompat{SendSessionAffinityHeaders: new(true), SupportsStrictMode: new(true), ThinkingFormat: "openrouter"}
			if !strings.HasPrefix(id, "openai/") {
				c.SupportsDeveloperRole = new(false)
			}
			return c
		},
	},
	qwenTokenPlanRule("qwen-token-plan", "alibaba-token-plan", "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"),
	qwenTokenPlanRule("qwen-token-plan-cn", "alibaba-token-plan-cn", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"),
	qwenTokenPlanRule("qwen-token-plan-individual", "alibaba-token-plan", "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"),
	{
		id: "together", modelsDev: "togetherai", api: APIOpenAICompletions, baseURL: "https://api.together.ai/v1",
		compat: func(id string, _ API, md *modelsDevModel) *ModelCompat {
			c := &ModelCompat{MaxTokensField: "max_tokens", SupportsDeveloperRole: new(false), SupportsLongCacheRetention: new(false), SupportsReasoningEffort: new(false), SupportsStore: new(false), SupportsStrictMode: new(false), ThinkingFormat: "together"}
			if md != nil && !md.Reasoning {
				c.ThinkingFormat = ""
			}
			// gpt-oss reasons with OpenAI's effort parameter.
			if strings.Contains(id, "gpt-oss") {
				c.ThinkingFormat = "openai"
				c.SupportsReasoningEffort = new(true)
			}
			return c
		},
	},
	{
		id: "vercel-ai-gateway", modelsDev: "vercel", api: APIAnthropicMessages, baseURL: "https://ai-gateway.vercel.sh",
		compat: staticCompat(ModelCompat{AllowEmptySignature: new(true)}),
	},
	{
		id: "xai", modelsDev: "xai", api: APIOpenAIResponses, baseURL: "https://api.x.ai/v1",
		compat: staticCompat(ModelCompat{SupportsLongCacheRetention: new(false)}),
	},
	xiaomiRule("xiaomi", "https://api.xiaomimimo.com/v1"),
	xiaomiRule("xiaomi-token-plan-ams", "https://token-plan-ams.xiaomimimo.com/v1"),
	xiaomiRule("xiaomi-token-plan-cn", "https://token-plan-cn.xiaomimimo.com/v1"),
	xiaomiRule("xiaomi-token-plan-sgp", "https://token-plan-sgp.xiaomimimo.com/v1"),
	zaiRule("zai", "zai", "https://api.z.ai/api/coding/paas/v4"),
	zaiRule("zai-coding-cn", "zhipuai-coding-plan", "https://open.bigmodel.cn/api/coding/paas/v4"),
}

const cloudflareGatewayBase = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}"

var (
	basetenTemplateThinking = regexp.MustCompile(`kimi-k2|nemotron|glm-4|glm-5(\.[0-2])?(-fast)?$`)
	fireworksCompletions    = regexp.MustCompile(`/(glm|kimi-k3)`)
	codexExcluded           = regexp.MustCompile(`-(pro|mini|nano|chat)|chat-latest|-image|-audio|-search|-realtime`)
)

func moonshotRule(id, base string) providerRule {
	return providerRule{
		id: id, modelsDev: id, api: APIOpenAICompletions, baseURL: base,
		compat: func(id string, _ API, _ *modelsDevModel) *ModelCompat {
			c := &ModelCompat{MaxTokensField: "max_tokens", SupportsDeveloperRole: new(false), SupportsMidConvoSystemMessages: new(true), SupportsReasoningEffort: new(false), SupportsStore: new(false), SupportsStrictMode: new(false), ThinkingFormat: "deepseek"}
			if strings.HasPrefix(strings.ToLower(id), "kimi-k3") {
				c.RequiresReasoningContentOnAssistantMessages = new(true)
				c.SupportsMidConvoToolAdditions = new(true)
				c.SupportsReasoningEffort = new(true)
				c.ThinkingFormat = "openai"
			}
			return c
		},
	}
}

// opencodeRule serves opencode's gateways: each model over the API its
// models.dev entry names (Anthropic, Responses, Google), else the
// OpenAI-compatible one.
func opencodeRule(id, base string) providerRule {
	return providerRule{
		id: id, modelsDev: id, api: APIOpenAICompletions, baseURL: base + "/v1", echoReasoning: true, deepseekFormat: id == "opencode-go",
		route: func(_ string, md *modelsDevModel) (API, string) {
			if md != nil && md.Provider != nil {
				switch md.Provider.NPM {
				case "@ai-sdk/anthropic":
					return APIAnthropicMessages, base
				case "@ai-sdk/openai":
					return APIOpenAIResponses, base + "/v1"
				case "@ai-sdk/google":
					return APIGoogleGenerativeAI, base + "/v1"
				}
			}
			return APIOpenAICompletions, base + "/v1"
		},
		compat: func(id string, api API, _ *modelsDevModel) *ModelCompat {
			switch api {
			case APIOpenAIResponses:
				c := gptResponsesCompat(id, false, 4)
				c.SessionAffinityFormat = "openai-nosession"
				return c
			case APIOpenAICompletions:
				c := compatMaxTokens
				return cloneCompat(&c)
			}
			return nil
		},
	}
}

func qwenTokenPlanRule(id, modelsDev, base string) providerRule {
	return providerRule{
		id: id, modelsDev: modelsDev, api: APIOpenAICompletions, baseURL: base,
		compat: func(_ string, _ API, md *modelsDevModel) *ModelCompat {
			return &ModelCompat{SupportsDeveloperRole: new(false), SupportsReasoningEffort: new(md != nil && len(md.efforts()) > 0), SupportsStore: new(false), SupportsStrictMode: new(true), ThinkingFormat: "qwen"}
		},
	}
}

func xiaomiRule(id, base string) providerRule {
	return providerRule{
		id: id, modelsDev: id, api: APIOpenAICompletions, baseURL: base,
		compat: staticCompat(ModelCompat{RequiresReasoningContentOnAssistantMessages: new(true), SupportsStrictMode: new(true), ThinkingFormat: "deepseek"}),
	}
}

func zaiRule(id, modelsDev, base string) providerRule {
	return providerRule{
		id: id, modelsDev: modelsDev, api: APIOpenAICompletions, baseURL: base,
		compat: func(_ string, _ API, md *modelsDevModel) *ModelCompat {
			return &ModelCompat{MaxTokensField: "max_tokens", SupportsDeveloperRole: new(false), SupportsReasoningEffort: new(md != nil && len(md.efforts()) > 0), SupportsStore: new(false), SupportsStrictMode: new(true), ThinkingFormat: "zai", ZaiToolStream: new(true)}
		},
	}
}

// gptResponsesCompat is the Responses API compat for OpenAI's GPT family:
// grammar (freeform) tools from GPT-5, and from 5.4 additional tools,
// mid-conversation system messages, and, on OpenAI's own endpoints, tool
// search.
func gptResponsesCompat(id string, toolSearch bool, additionalFrom int) *ModelCompat {
	c := &ModelCompat{}
	if gptAtLeast(id, 5, 0) {
		c.SupportsOpenAIGrammarTools = new(true)
	}
	if gptAtLeast(id, 5, 4) && !strings.Contains(id, "-nano") {
		c.SupportsMidConvoSystemMessages = new(true)
		if gptAtLeast(id, 5, additionalFrom) {
			c.SupportsAdditionalTools = new(true)
		}
		if toolSearch {
			c.SupportsToolSearch = new(true)
		}
	}
	return c
}

// build composes one model: provider rules, models.dev metadata, the
// provider list's details on top, then the family rules.
func (r *providerRule) build(id string, md *modelsDevModel, lm *LiveModel) KnownModel {
	m := KnownModel{ID: id, Provider: r.id, DisplayName: id, API: r.api, BaseURL: r.baseURL}
	if r.route != nil {
		m.API, m.BaseURL = r.route(id, md)
	}
	m.Headers = cloneStringMap(r.headers)
	m.PromptCache = maps.Clone(r.promptCache)

	var efforts []string
	input := []string{"text"}
	toggle := false
	m.PriceUnknown = true
	if md != nil {
		m.DisplayName = cmpOr(md.Name, id)
		m.Released = md.ReleaseDate
		m.Reasoning = md.Reasoning
		m.ContextWindow = md.Limit.Context
		m.MaxOutputTokens = md.Limit.Output
		if slices.Contains(md.Modalities.Input, "image") {
			input = append(input, "image")
		}
		efforts = md.efforts()
		toggle = md.hasOption("toggle") || md.hasOption("budget_tokens")
		if md.Cost != nil {
			m.InputCostPerMTokens, m.OutputCostPerMTokens = md.Cost.Input, md.Cost.Output
			m.CacheReadCost, m.CacheWriteCost = md.Cost.CacheRead, md.Cost.CacheWrite
			for _, t := range md.Cost.Tiers {
				if t.Tier.Type == "context" && t.Tier.Size > 0 {
					m.Tiers = append(m.Tiers, CostTier{InputTokensAbove: t.Tier.Size, InputCostPer1M: t.Input, OutputCostPer1M: t.Output, CacheReadCostPer1M: t.CacheRead, CacheWriteCostPer1M: t.CacheWrite})
				}
			}
			m.PriceUnknown = false
			if r.tierWindow {
				for _, t := range m.Tiers {
					if t.InputTokensAbove > 0 && t.InputTokensAbove < m.ContextWindow {
						m.ContextWindow = t.InputTokensAbove
					}
				}
			}
		}
	}
	if lm != nil {
		m.DisplayName = cmpOr(lm.Name, m.DisplayName)
		m.Released = cmpOr(lm.Released, m.Released)
		if lm.Context > 0 {
			m.ContextWindow = lm.Context
		}
		if lm.MaxOutput > 0 {
			m.MaxOutputTokens = lm.MaxOutput
		}
		if len(lm.Input) > 0 {
			input = []string{"text"}
			if slices.Contains(lm.Input, "image") {
				input = append(input, "image")
			}
		}
		if len(lm.Efforts) > 0 {
			efforts = lm.Efforts
			m.Reasoning = true
		}
		applyLivePrice(&m, *lm)
	}
	m.Capabilities = input

	if slices.Contains(input, "image") {
		images := &ModelImageInputLimits{Resize: new(defaultImageResize), MaxPerMessage: r.imagesPerMessage}
		if r.imagesPerRequest != nil {
			images.MaxPerRequest = r.imagesPerRequest(m.ContextWindow)
		}
		m.InputLimits = &ModelInputLimits{MaxRequestBytes: r.maxRequestBytes, Images: images}
	}
	if r.compat != nil {
		m.Compat = r.compat(id, m.API, md)
	}
	_, _, claude := claudeVersion(id)
	m.ThinkingLevelMap = thinkingMap(claude, m.Reasoning, efforts, toggle, r.minimalAsLow)
	applyFamilyRules(&m, md, lm)
	if m.Compat != nil && reflect.ValueOf(*m.Compat).IsZero() {
		m.Compat = nil
	}
	return m
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// thinkingMap derives how wopr's thinking levels map onto a model's
// reasoning efforts. Claude takes low through high on every thinking
// model, so only xhigh and max need declaring; other models get the full
// map: a level the model lacks is unsupported, and "off" sends the model's
// "none" effort or, without one, is unsupported unless thinking can be
// toggled.
func thinkingMap(claude, reasoning bool, efforts []string, toggle, minimalAsLow bool) ThinkingLevelMap {
	if !reasoning || len(efforts) == 0 {
		return nil
	}
	has := func(v string) bool { return slices.Contains(efforts, v) }
	out := ThinkingLevelMap{}
	if claude {
		for _, level := range []ThinkingLevel{ThinkingXHigh, ThinkingMax} {
			if has(string(level)) {
				out[level] = new(string(level))
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	for _, level := range []ThinkingLevel{ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax} {
		switch {
		case has(string(level)):
			out[level] = new(string(level))
		case level == ThinkingMinimal && minimalAsLow && has("low"):
			out[level] = new("low")
		default:
			out[level] = nil
		}
	}
	switch {
	case has("none"):
		out[ThinkingOff] = new("none")
	case !toggle:
		out[ThinkingOff] = nil
	}
	return out
}

// ─── Family rules ────────────────────────────────────────────────────────────

var (
	claudeRE = regexp.MustCompile(`claude-(opus|sonnet|haiku|fable)-(\d+)(?:[.-](\d))?(?:$|[^0-9])`)
	gptRE    = regexp.MustCompile(`(?:^|[/.])gpt-(\d+)(?:\.(\d+))?`)
)

// familyID is a model id without its host's prefixes: "us.anthropic.
// claude-opus-4-8" and "anthropic/claude-opus-4.8" are both claude-opus-4-8.
func familyID(id string) string {
	id = strings.ToLower(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// claudeVersion parses a Claude model's family and version (4.8 → 48).
func claudeVersion(id string) (family string, version int, ok bool) {
	sub := claudeRE.FindStringSubmatch(familyID(id))
	if sub == nil {
		return "", 0, false
	}
	major, _ := strconv.Atoi(sub[2])
	minor := 0
	if sub[3] != "" {
		minor, _ = strconv.Atoi(sub[3])
	}
	return sub[1], major*10 + minor, true
}

// gptAtLeast reports whether id is a GPT model of at least major.minor.
func gptAtLeast(id string, major, minor int) bool {
	sub := gptRE.FindStringSubmatch(strings.ToLower(id))
	if sub == nil {
		return false
	}
	ma, _ := strconv.Atoi(sub[1])
	mi := 0
	if sub[2] != "" {
		mi, _ = strconv.Atoi(sub[2])
	}
	return ma > major || ma == major && mi >= minor
}

// applyFamilyRules sets what a model family needs wherever it's served.
func applyFamilyRules(m *KnownModel, md *modelsDevModel, lm *LiveModel) {
	if family, v, ok := claudeVersion(m.ID); ok {
		claudeRules(m, family, v, md, lm)
	}
	if md != nil && md.Temperature != nil && !*md.Temperature && m.API == APIAnthropicMessages {
		compat(m).SupportsTemperature = new(false)
	}
	if m.API != APIOpenAICompletions {
		return
	}
	fid := familyID(m.ID)
	if rule, _ := ruleFor(m.Provider); rule != nil && deepseekStyle.MatchString(fid) {
		if rule.echoReasoning {
			compat(m).RequiresReasoningContentOnAssistantMessages = new(true)
		}
		if rule.deepseekFormat && compat(m).ThinkingFormat == "" {
			m.Compat.ThinkingFormat = "deepseek"
		}
	}
	// Kimi K3 takes system messages and new tools mid-conversation.
	if strings.HasPrefix(fid, "kimi-k3") {
		c := compat(m)
		c.SupportsMidConvoSystemMessages = new(true)
		c.SupportsMidConvoToolAdditions = new(true)
	}
	// OpenAI's GPT-5.4+ through a chat gateway takes system messages
	// mid-conversation.
	if gptAtLeast(fid, 5, 4) && m.Provider == "openrouter" {
		compat(m).SupportsMidConvoSystemMessages = new(true)
	}
}

// deepseekStyle are the open models that reason with DeepSeek's format and
// need their reasoning sent back.
var deepseekStyle = regexp.MustCompile(`deepseek-v4|mimo-v2`)

func compat(m *KnownModel) *ModelCompat {
	if m.Compat == nil {
		m.Compat = &ModelCompat{}
	}
	return m.Compat
}

// claudeRules: Fable and Opus 5+ can't turn thinking off; from 4.6 Claude
// thinks adaptively; Opus from 4.7 rejects temperature; Fable and Opus
// from 4.8 take system messages mid-conversation, and on Anthropic's own
// API tool changes too; Fable 5.1 and Opus 5 take effort changes there and
// through OpenRouter.
func claudeRules(m *KnownModel, family string, v int, md *modelsDevModel, lm *LiveModel) {
	if (family == "fable" || family == "opus" && v >= 50) && m.Reasoning && (md == nil || !md.hasOption("toggle")) {
		if m.ThinkingLevelMap == nil {
			m.ThinkingLevelMap = ThinkingLevelMap{}
		}
		m.ThinkingLevelMap[ThinkingOff] = nil
	}
	if m.API != APIAnthropicMessages {
		return
	}
	adaptive := v >= 46
	if lm != nil && lm.Adaptive != nil {
		adaptive = *lm.Adaptive
	}
	if adaptive {
		compat(m).ForceAdaptiveThinking = new(true)
	}
	if family == "opus" && v >= 47 {
		compat(m).SupportsTemperature = new(false)
	}
	if family == "fable" || family == "opus" && v >= 48 {
		compat(m).SupportsMidConvoSystemMessages = new(true)
	}
	if m.Provider != "anthropic" && m.Provider != "openrouter" {
		return
	}
	if family == "fable" && v >= 51 || family == "opus" && v >= 50 {
		compat(m).SupportsMidConvoEffort = new(true)
	}
	if m.Provider == "anthropic" && (family == "fable" || family == "opus" && v >= 48) {
		compat(m).SupportsMidConvoToolChanges = new(true)
	}
}
