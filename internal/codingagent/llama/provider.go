package llama

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// LlamaProviderID is the llama.cpp provider ID.
const LlamaProviderID = "llama.cpp"

// DefaultLlamaServerURL is the default llama.cpp router URL.
const DefaultLlamaServerURL = "http://127.0.0.1:8080"

const openAICompletionsAPI = "openai-completions"

// ThinkingLevelMap is a published model's thinking-level map, in a fixed key
// order; nil entries serialize as null.
type ThinkingLevelMap struct {
	Off     *string `json:"off"`
	Minimal *string `json:"minimal"`
	Low     *string `json:"low"`
	Medium  *string `json:"medium"`
	High    *string `json:"high"`
	XHigh   *string `json:"xhigh"`
}

// ModelCost is a published model's cost.
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ModelCompat is a published model's openai-completions compat object.
type ModelCompat struct {
	SupportsStore            bool   `json:"supportsStore"`
	SupportsDeveloperRole    bool   `json:"supportsDeveloperRole"`
	SupportsReasoningEffort  bool   `json:"supportsReasoningEffort"`
	SupportsUsageInStreaming bool   `json:"supportsUsageInStreaming"`
	SupportsStrictMode       bool   `json:"supportsStrictMode"`
	MaxTokensField           string `json:"maxTokensField"`
	ThinkingFormat           string `json:"thinkingFormat,omitempty"`
}

// Model is the openai-completions model the provider publishes and persists,
// in a fixed field order.
type Model struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	API              string            `json:"api"`
	Provider         string            `json:"provider"`
	BaseURL          string            `json:"baseUrl"`
	Reasoning        bool              `json:"reasoning"`
	ThinkingLevelMap *ThinkingLevelMap `json:"thinkingLevelMap,omitempty"`
	Input            []string          `json:"input"`
	Cost             ModelCost         `json:"cost"`
	ContextWindow    int               `json:"contextWindow"`
	MaxTokens        int               `json:"maxTokens"`
	Compat           ModelCompat       `json:"compat"`
}

// AuthContext is the environment auth reads. Env reports ok=false for an
// unset or blank variable.
type AuthContext struct {
	Env func(name string) (string, bool)
}

// DefaultAuthContext reads the process environment.
func DefaultAuthContext() AuthContext {
	return AuthContext{Env: func(name string) (string, bool) {
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			return "", false
		}
		return value, true
	}}
}

// AuthPrompt is a text or secret login prompt, the only kinds this provider
// asks for.
type AuthPrompt struct {
	Type        string
	Message     string
	Placeholder string
}

// AuthInteraction is a login interaction; Ctx carries its cancellation.
type AuthInteraction struct {
	Ctx    context.Context
	Prompt func(AuthPrompt) (string, error)
}

// ModelAuth is request auth for a model.
type ModelAuth struct {
	APIKey  string
	BaseURL string
}

// AuthResult is resolved request auth plus its environment.
type AuthResult struct {
	Auth   ModelAuth
	Env    map[string]string
	Source string
}

// AuthCheck reports where available auth comes from.
type AuthCheck struct {
	Source string
	Type   string
}

// ModelsPublication is a catalog update; a nil Persist leaves
// storage unchanged.
type ModelsPublication struct {
	Persist *ai.ModelsStoreEntry
	Update  func()
}

// RefreshModelsContext is the input to a catalog refresh.
type RefreshModelsContext struct {
	Ctx          context.Context
	Credential   *ai.Credential
	Stored       *ai.ModelsStoreEntry
	Publish      func(ModelsPublication) (bool, error)
	AllowNetwork bool
}

// Provider is the llama.cpp provider CreateLlamaProvider returns. Its
// api-key auth is login, check, and resolve; its catalog is the
// controller's. Streaming uses the host's openai-completions implementation
// for API.
type Provider struct {
	ID      string
	Name    string
	BaseURL string
	API     string
}

// LlamaProviderController owns the provider and its current catalog.
type LlamaProviderController struct {
	Provider *Provider

	mu     sync.Mutex
	models []Model
}

func credentialServerURL(credential *ai.Credential) (string, error) {
	if credential == nil {
		return "", nil
	}
	value := credential.Env["LLAMA_BASE_URL"]
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return NormalizeLlamaServerURL(value)
}

func resolveServerURL(auth AuthContext, credential *ai.Credential) (string, error) {
	configured, err := credentialServerURL(credential)
	if err != nil {
		return "", err
	}
	if configured == "" {
		value, _ := auth.Env("LLAMA_BASE_URL")
		configured = strings.TrimSpace(value)
	}
	if configured == "" {
		return "", nil
	}
	return NormalizeLlamaServerURL(configured)
}

func modelIsSelectable(model LlamaModelInfo, routerAutoload bool) bool {
	switch model.Status.Value {
	case LlamaModelStatusLoaded:
		return true
	case LlamaModelStatusSleeping:
		// llama.cpp reports idle-slept models as "sleeping"; requests wake them automatically.
		return true
	}
	// Unloaded presets are routable only when llama.cpp router autoload can load them on first use.
	return routerAutoload && model.Status.Value == LlamaModelStatusUnloaded && !model.Status.Failed && model.Source == "preset"
}

func routerAutoloadEnabled(ctx context.Context, client *LlamaClient, catalog []LlamaModelInfo) bool {
	if !slices.ContainsFunc(catalog, func(model LlamaModelInfo) bool {
		return model.Status.Value == LlamaModelStatusUnloaded && model.Source == "preset"
	}) {
		return false
	}
	props, err := client.Props(ctx, "")
	return err == nil && props.ModelsAutoload
}

func toModel(model LlamaModelInfo, serverURL string, props *LlamaServerProps) Model {
	var reported *float64
	if model.Meta != nil {
		reported = model.Meta.NCtx
		if reported == nil {
			reported = model.Meta.NCtxTrain
		}
	}
	contextWindow := ai.DefaultContextWindow
	if reported != nil && *reported > 0 {
		contextWindow = int(*reported)
	}
	reasoning := props != nil && strings.Contains(props.ChatTemplate, "enable_thinking")
	inferenceURL, _ := LlamaInferenceURL(serverURL)
	input := []string{"text"}
	if model.Architecture != nil && slices.Contains(model.Architecture.InputModalities, "image") {
		input = []string{"text", "image"}
	}
	result := Model{
		ID:            model.ID,
		Name:          model.ID,
		API:           openAICompletionsAPI,
		Provider:      LlamaProviderID,
		BaseURL:       inferenceURL,
		Reasoning:     reasoning,
		Input:         input,
		ContextWindow: contextWindow,
		MaxTokens:     contextWindow,
		Compat:        ModelCompat{SupportsUsageInStreaming: true, MaxTokensField: "max_tokens"},
	}
	if reasoning {
		off, medium := "off", "medium"
		result.ThinkingLevelMap = &ThinkingLevelMap{Off: &off, Medium: &medium}
		result.Compat.ThinkingFormat = "qwen-chat-template"
	}
	return result
}

// SetCatalog replaces the published models with catalog.
func (c *LlamaProviderController) SetCatalog(catalog []LlamaModelInfo, serverURL string, routerAutoload bool) {
	var models []Model
	for _, model := range catalog {
		if modelIsSelectable(model, routerAutoload) {
			models = append(models, toModel(model, serverURL, nil))
		}
	}
	c.setModels(models)
}

func (c *LlamaProviderController) setModels(models []Model) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
}

// GetModels returns the published models.
func (c *LlamaProviderController) GetModels() []Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.models)
}

// CreateLlamaProvider builds the llama.cpp provider.
func CreateLlamaProvider() *LlamaProviderController {
	controller := &LlamaProviderController{}
	defaultBaseURL, _ := LlamaInferenceURL(DefaultLlamaServerURL)
	controller.Provider = &Provider{
		ID:      LlamaProviderID,
		Name:    "llama.cpp",
		BaseURL: defaultBaseURL,
		API:     openAICompletionsAPI,
	}
	return controller
}

func login(interaction AuthInteraction) (ai.Credential, error) {
	envURL, envSet := os.LookupEnv("LLAMA_BASE_URL")
	placeholder := DefaultLlamaServerURL
	if envSet {
		placeholder = envURL
	}
	enteredURL, err := interaction.Prompt(AuthPrompt{Type: "text", Message: "llama.cpp server URL", Placeholder: placeholder})
	if err != nil {
		return ai.Credential{}, err
	}
	serverURL, err := NormalizeLlamaServerURL(cmp.Or(strings.TrimSpace(enteredURL), envURL, DefaultLlamaServerURL))
	if err != nil {
		return ai.Credential{}, err
	}
	enteredKey, err := interaction.Prompt(AuthPrompt{Type: "secret", Message: "API key (optional)"})
	if err != nil {
		return ai.Credential{}, err
	}
	apiKey := strings.TrimSpace(enteredKey)
	client, err := NewLlamaClient(serverURL, apiKey)
	if err != nil {
		return ai.Credential{}, err
	}
	if _, err := client.List(interaction.Ctx, false); err != nil {
		return ai.Credential{}, err
	}
	return ai.Credential{Type: ai.CredentialAPIKey, Key: apiKey, Env: map[string]string{"LLAMA_BASE_URL": serverURL}}, nil
}

func authSource(credential *ai.Credential) string {
	if credential != nil {
		return "stored credential"
	}
	return "LLAMA_BASE_URL"
}

func check(_ context.Context, auth AuthContext, credential *ai.Credential) (*AuthCheck, error) {
	serverURL, err := resolveServerURL(auth, credential)
	if err != nil || serverURL == "" {
		return nil, err
	}
	return &AuthCheck{Type: "api_key", Source: authSource(credential)}, nil
}

func resolve(_ context.Context, auth AuthContext, credential *ai.Credential) (*AuthResult, error) {
	serverURL, err := resolveServerURL(auth, credential)
	if err != nil || serverURL == "" {
		return nil, err
	}
	apiKey := "local"
	if credential != nil && credential.Key != "" {
		apiKey = credential.Key
	} else if value, ok := auth.Env("LLAMA_API_KEY"); ok {
		apiKey = value
	}
	env := map[string]string{}
	if credential != nil {
		maps.Copy(env, credential.Env)
	}
	env["LLAMA_BASE_URL"] = serverURL
	inferenceURL, _ := LlamaInferenceURL(serverURL)
	return &AuthResult{Auth: ModelAuth{APIKey: apiKey, BaseURL: inferenceURL}, Env: env, Source: authSource(credential)}, nil
}

// restoredModels filters the stored models; entries that do not decode
// as a model are skipped.
func restoredModels(stored *ai.ModelsStoreEntry) []Model {
	var models []Model
	for _, raw := range stored.Models {
		var model Model
		if json.Unmarshal(raw, &model) == nil && model.Provider == LlamaProviderID && model.API == openAICompletionsAPI {
			models = append(models, model)
		}
	}
	return models
}

// RefreshModels restores the stored catalog, then, when the refresh allows
// the network and carries an api-key credential, fetches the live one.
func (c *LlamaProviderController) RefreshModels(refresh RefreshModelsContext) error {
	if refresh.Stored != nil {
		restored := restoredModels(refresh.Stored)
		published, err := refresh.Publish(ModelsPublication{Update: func() { c.setModels(restored) }})
		if err != nil || !published {
			return err
		}
	}
	ctx := refresh.Ctx
	if !refresh.AllowNetwork || ctx.Err() != nil || refresh.Credential == nil || refresh.Credential.Type != ai.CredentialAPIKey {
		return nil
	}
	serverURL, err := credentialServerURL(refresh.Credential)
	if err != nil || serverURL == "" {
		return err
	}
	client, err := NewLlamaClient(serverURL, refresh.Credential.Key)
	if err != nil {
		return err
	}
	catalog, err := client.List(ctx, false)
	if err != nil || ctx.Err() != nil {
		return err
	}
	routerAutoload := routerAutoloadEnabled(ctx, client, catalog)
	if ctx.Err() != nil {
		return nil
	}
	refreshed, err := classifyModels(ctx, client, catalog, serverURL, routerAutoload)
	if err != nil || ctx.Err() != nil {
		return err
	}
	encoded := make([]json.RawMessage, 0, len(refreshed))
	for _, model := range refreshed {
		raw, err := json.Marshal(model)
		if err != nil {
			return err
		}
		encoded = append(encoded, raw)
	}
	checkedAt := float64(time.Now().UnixMilli())
	_, err = refresh.Publish(ModelsPublication{
		Persist: &ai.ModelsStoreEntry{Models: encoded, CheckedAt: &checkedAt},
		Update:  func() { c.setModels(refreshed) },
	})
	return err
}

// classifyModels classifies selectable models concurrently: one request per
// loaded model, results in catalog order, first rejection wins.
func classifyModels(ctx context.Context, client *LlamaClient, catalog []LlamaModelInfo, serverURL string, routerAutoload bool) ([]Model, error) {
	var selectable []LlamaModelInfo
	for _, model := range catalog {
		if modelIsSelectable(model, routerAutoload) {
			selectable = append(selectable, model)
		}
	}
	results := make([]Model, len(selectable))
	groupCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var group sync.WaitGroup
	var once sync.Once
	var firstErr error
	for index, model := range selectable {
		// Only loaded models expose their template without side effects. Unloaded autoload presets
		// would need to be loaded, while querying sleeping models may wake them. Those models remain
		// unclassified until they are loaded or woken and a later catalog refresh discovers them.
		if model.Status.Value != LlamaModelStatusLoaded {
			results[index] = toModel(model, serverURL, nil)
			continue
		}
		group.Go(func() {
			props, err := client.Props(groupCtx, model.ID)
			if err != nil {
				once.Do(func() { firstErr = err; cancel(err) })
				return
			}
			results[index] = toModel(model, serverURL, &props)
		})
	}
	group.Wait()
	return results, firstErr
}
