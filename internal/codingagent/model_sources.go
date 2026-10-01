package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// Model sources on disk, under the agent dir:
//
//	models-dev.json    models.dev's data, trimmed to what wopr reads
//	models-store.json  each connected provider's own model list (the
//	                   store llama.cpp's catalog also lives in)
//
// Both refresh in the background once a day; startup only reads them.

const (
	modelsDevFile    = "models-dev.json"
	modelSourceTTL   = 24 * time.Hour
	modelsDevURL     = "https://models.dev/api.json"
	liveListTimeout  = 20 * time.Second
	modelsDevTimeout = 30 * time.Second
)

func modelsStore(agentDir string) *ai.FileModelsStore {
	return ai.NewFileModelsStore(filepath.Join(agentDir, "models-store.json"))
}

// LoadModelSources hands the cached models.dev copy and provider lists to
// the model database. It reads only local files.
func LoadModelSources(agentDir string) {
	if agentDir == "" {
		return
	}
	if raw, err := os.ReadFile(filepath.Join(agentDir, modelsDevFile)); err == nil {
		_ = ai.SetModelsDev(raw) // a bad copy leaves the embedded snapshot
	}
	lists := map[string][]ai.LiveModel{}
	for provider, entry := range storedLists(agentDir) {
		lists[provider] = entry.models
	}
	ai.SetAllLiveModels(lists)
}

type storedList struct {
	models  []ai.LiveModel
	checked time.Time
}

// storedLists are the provider lists in the models store, for providers
// wopr has rules for.
func storedLists(agentDir string) map[string]storedList {
	out := map[string]storedList{}
	if _, err := os.Stat(filepath.Join(agentDir, "models-store.json")); err != nil {
		return out
	}
	entries, err := modelsStore(agentDir).ReadAll(context.Background())
	if err != nil {
		return out
	}
	for provider, entry := range entries {
		if !ai.HasModelRules(provider) || len(entry.Models) == 0 {
			continue
		}
		var list storedList
		for _, raw := range entry.Models {
			var m ai.LiveModel
			if json.Unmarshal(raw, &m) == nil && m.ID != "" {
				list.models = append(list.models, m)
			}
		}
		if entry.CheckedAt != nil {
			list.checked = time.Unix(int64(*entry.CheckedAt), 0)
		}
		if len(list.models) > 0 {
			out[provider] = list
		}
	}
	return out
}

// StoreLiveModels records a provider's freshly read model list: in the
// model database now and in the models store for later runs.
func StoreLiveModels(agentDir, provider string, models []ai.LiveModel) {
	if len(models) == 0 {
		return
	}
	ai.SetLiveModels(provider, models)
	if agentDir == "" {
		return
	}
	entry := ai.ModelsStoreEntry{CheckedAt: new(float64(time.Now().Unix()))}
	for _, m := range models {
		if raw, err := json.Marshal(m); err == nil {
			entry.Models = append(entry.Models, raw)
		}
	}
	_ = modelsStore(agentDir).Write(context.Background(), provider, entry)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RefreshModelSources refreshes what's older than a day: models.dev, and
// the model list of every provider with a stored or environment
// credential. It never refreshes a sign-in: a provider whose token has
// expired waits for the next run that signs in anyway.
func RefreshModelSources(ctx context.Context, agentDir string, auth *ai.AuthStorage) {
	if agentDir == "" {
		return
	}
	if stale(filepath.Join(agentDir, modelsDevFile)) {
		if raw, err := fetchModelsDev(ctx); err == nil {
			if trimmed, err := ai.TrimModelsDev(raw); err == nil && ai.SetModelsDev(trimmed) == nil {
				_ = writeAtomic(filepath.Join(agentDir, modelsDevFile), trimmed)
			}
		}
	}
	stored := storedLists(agentDir)
	for _, provider := range connectedProviders(auth) {
		if list, ok := stored[provider]; ok && time.Since(list.checked) < modelSourceTTL {
			continue
		}
		key, oauth, ok := providerKeyNoRefresh(auth, provider)
		if !ok {
			continue
		}
		models, err := fetchLiveModels(ctx, provider, key, oauth)
		if err != nil || len(models) == 0 {
			continue
		}
		StoreLiveModels(agentDir, provider, models)
	}
}

func stale(path string) bool {
	info, err := os.Stat(path)
	return err != nil || time.Since(info.ModTime()) >= modelSourceTTL
}

func fetchModelsDev(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsDevTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := setupHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// connectedProviders are the providers wopr has rules for and a
// credential for, stored or in the environment.
func connectedProviders(auth *ai.AuthStorage) []string {
	seen := map[string]bool{}
	var out []string
	if auth != nil {
		if creds, err := auth.Load(); err == nil {
			for provider := range creds {
				if ai.HasModelRules(provider) && !seen[provider] {
					seen[provider] = true
					out = append(out, provider)
				}
			}
		}
	}
	for _, provider := range ai.ListProviders() {
		if !seen[provider] && ai.GetEnvAPIKey(provider, nil) != "" {
			seen[provider] = true
			out = append(out, provider)
		}
	}
	slices.Sort(out)
	return out
}

// providerKeyNoRefresh is a provider's API key, or its sign-in token while
// it's still good for a few minutes.
func providerKeyNoRefresh(auth *ai.AuthStorage, provider string) (key string, oauth, ok bool) {
	if auth != nil {
		if cred, found, err := auth.GetRaw(provider); err == nil && found {
			switch cred.Type {
			case ai.CredentialOAuth:
				if cred.Access != "" && time.UnixMilli(cred.Expires).After(time.Now().Add(5*time.Minute)) {
					return cred.Access, true, true
				}
				return "", false, false
			case ai.CredentialAPIKey:
				if c, found, err := auth.Get(provider); err == nil && found && c.Key != "" {
					return c.Key, false, true
				}
			}
		}
	}
	if key := ai.GetEnvAPIKey(provider, nil); key != "" {
		return key, false, true
	}
	return "", false, false
}

// errNoModelList reports a provider whose API has no model list wopr can
// read.
var errNoModelList = errors.New("this provider doesn't publish a model list")

// fetchLiveModels asks a provider for its models.
func fetchLiveModels(ctx context.Context, provider, key string, oauth bool) ([]ai.LiveModel, error) {
	ctx, cancel := context.WithTimeout(ctx, liveListTimeout)
	defer cancel()
	api, base := ai.ProviderEndpoint(provider)
	if base == "" || strings.Contains(base, "{") {
		return nil, errNoModelList
	}
	switch api {
	case ai.APIAnthropicMessages:
		return anthropicModels(ctx, strings.TrimSuffix(base, "/v1"), key, oauth)
	case ai.APIGoogleGenerativeAI:
		return googleModels(ctx, base, key)
	case ai.APIOpenAICodexResponses:
		return codexModels(ctx, base, key)
	case ai.APIOpenAICompletions, ai.APIOpenAIResponses, ai.APIMistralConversations:
		return openAIStyleModels(ctx, base, key)
	}
	return nil, errNoModelList
}

// getJSON sends one GET and decodes the reply.
func getJSON(ctx context.Context, rawURL string, header http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header = header
	resp, err := setupHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return readError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

type supported struct {
	Supported bool `json:"supported"`
}

// anthropicModels reads Anthropic's model list, page by page: limits,
// effort levels, adaptive thinking, and image input. A subscription's
// sign-in token is sent as a bearer token with the OAuth beta header; an
// API key as x-api-key.
func anthropicModels(ctx context.Context, base, key string, oauth bool) ([]ai.LiveModel, error) {
	header := http.Header{}
	header.Set("anthropic-version", "2023-06-01")
	if oauth || strings.Contains(key, "sk-ant-oat") {
		header.Set("Authorization", "Bearer "+key)
		header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		header.Set("x-api-key", key)
	}
	var out []ai.LiveModel
	after := ""
	for range 20 {
		query := url.Values{"limit": {"1000"}}
		if after != "" {
			query.Set("after_id", after)
		}
		var page struct {
			Data []struct {
				ID             string `json:"id"`
				DisplayName    string `json:"display_name"`
				CreatedAt      string `json:"created_at"`
				MaxInputTokens int    `json:"max_input_tokens"`
				MaxTokens      int    `json:"max_tokens"`
				Capabilities   struct {
					Effort     map[string]json.RawMessage `json:"effort"`
					ImageInput supported                  `json:"image_input"`
					Thinking   struct {
						Supported bool `json:"supported"`
						Types     struct {
							Adaptive supported `json:"adaptive"`
						} `json:"types"`
					} `json:"thinking"`
				} `json:"capabilities"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := getJSON(ctx, base+"/v1/models?"+query.Encode(), header, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			lm := ai.LiveModel{ID: m.ID, Name: m.DisplayName, Context: m.MaxInputTokens, MaxOutput: m.MaxTokens, Released: dateOnly(m.CreatedAt)}
			if m.Capabilities.ImageInput.Supported {
				lm.Input = []string{"text", "image"}
			} else if m.MaxInputTokens > 0 {
				lm.Input = []string{"text"}
			}
			for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
				var s supported
				if raw, ok := m.Capabilities.Effort[level]; ok && json.Unmarshal(raw, &s) == nil && s.Supported {
					lm.Efforts = append(lm.Efforts, level)
				}
			}
			if m.Capabilities.Thinking.Supported {
				lm.Adaptive = new(m.Capabilities.Thinking.Types.Adaptive.Supported)
			}
			out = append(out, lm)
		}
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// googleModels reads Google's model list, keeping the models that
// generate content.
func googleModels(ctx context.Context, base, key string) ([]ai.LiveModel, error) {
	var out []ai.LiveModel
	token := ""
	for range 20 {
		query := url.Values{"key": {key}, "pageSize": {"1000"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		var page struct {
			Models []struct {
				Name             string   `json:"name"`
				DisplayName      string   `json:"displayName"`
				InputTokenLimit  int      `json:"inputTokenLimit"`
				OutputTokenLimit int      `json:"outputTokenLimit"`
				Methods          []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := getJSON(ctx, base+"/models?"+query.Encode(), http.Header{}, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Models {
			if slices.Contains(m.Methods, "generateContent") {
				out = append(out, ai.LiveModel{ID: strings.TrimPrefix(m.Name, "models/"), Name: m.DisplayName, Context: m.InputTokenLimit, MaxOutput: m.OutputTokenLimit})
			}
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	return out, nil
}

// codexClientVersion is the client version the ChatGPT model list filters
// by; a high one lists every model the subscription has.
const codexClientVersion = "99.0.0"

// codexModels reads the ChatGPT subscription's model list.
func codexModels(ctx context.Context, base, token string) ([]ai.LiveModel, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("originator", "codex_cli_rs")
	if account := ai.CodexAccountID(token); account != "" {
		header.Set("chatgpt-account-id", account)
	}
	var list struct {
		Models []struct {
			Slug            string   `json:"slug"`
			DisplayName     string   `json:"display_name"`
			ContextWindow   int      `json:"context_window"`
			InputModalities []string `json:"input_modalities"`
			Visibility      string   `json:"visibility"`
			Reasoning       []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := getJSON(ctx, strings.TrimRight(base, "/")+"/codex/models?client_version="+codexClientVersion, header, &list); err != nil {
		return nil, err
	}
	var out []ai.LiveModel
	for _, m := range list.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		lm := ai.LiveModel{ID: m.Slug, Name: m.DisplayName, Context: m.ContextWindow, Input: m.InputModalities}
		for _, r := range m.Reasoning {
			if slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, r.Effort) {
				lm.Efforts = append(lm.Efforts, r.Effort)
			}
		}
		out = append(out, lm)
	}
	return out, nil
}

// openAIStyleModels reads /models on an OpenAI-compatible API, keeping chat
// models and whatever the list says about limits, input, and prices
// (OpenRouter's carries all three).
func openAIStyleModels(ctx context.Context, base, key string) ([]ai.LiveModel, error) {
	header := http.Header{}
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := getJSON(ctx, strings.TrimRight(base, "/")+"/models", header, &list); err != nil {
		return nil, err
	}
	var out []ai.LiveModel
	for _, entry := range list.Data {
		id, _ := entry["id"].(string)
		if id == "" || !chatModel(id) {
			continue
		}
		lm := ai.LiveModel{ID: id}
		lm.Name, _ = entry["name"].(string)
		if created := jsonInt(entry["created"]); created > 0 {
			lm.Released = time.Unix(int64(created), 0).UTC().Format(time.DateOnly)
		}
		for _, k := range []string{"context_length", "context_window", "max_model_len", "max_context_length"} {
			if n := jsonInt(entry[k]); n > 0 {
				lm.Context = n
				break
			}
		}
		if top, ok := entry["top_provider"].(map[string]any); ok {
			lm.MaxOutput = jsonInt(top["max_completion_tokens"])
		}
		if arch, ok := entry["architecture"].(map[string]any); ok {
			if mods, ok := arch["input_modalities"].([]any); ok {
				for _, m := range mods {
					if s, ok := m.(string); ok && (s == "text" || s == "image") {
						lm.Input = append(lm.Input, s)
					}
				}
			}
		}
		if pricing, ok := entry["pricing"].(map[string]any); ok {
			lm.InputCost = perMillion(pricing["prompt"])
			lm.OutputCost = perMillion(pricing["completion"])
			lm.CacheReadCost = perMillion(pricing["input_cache_read"])
			lm.CacheWriteCost = perMillion(pricing["input_cache_write"])
		}
		out = append(out, lm)
	}
	if len(out) == 0 {
		return nil, errors.New("the provider listed no chat models")
	}
	return out, nil
}

// perMillion turns OpenRouter's per-token price string into a
// per-million-token price; nil when absent or negative (a variable price).
func perMillion(v any) *float64 {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return nil
	}
	f *= 1e6
	return &f
}

// chatModel reports whether a listed model id looks like one that chats,
// leaving out embeddings, speech, image, and moderation models that
// OpenAI-style lists include.
func chatModel(id string) bool {
	id = strings.ToLower(id)
	for _, word := range []string{"embedding", "tts", "whisper", "dall-e", "moderation", "transcribe", "audio", "realtime", "image"} {
		if strings.Contains(id, word) {
			return false
		}
	}
	return true
}

// liveToRemote is a provider list entry as setup's checklist shows it.
func liveToRemote(models []ai.LiveModel) []remoteModel {
	out := make([]remoteModel, 0, len(models))
	for _, m := range models {
		out = append(out, remoteModel{ID: m.ID, Name: m.Name, Context: m.Context, MaxOutput: m.MaxOutput})
	}
	return out
}

// dateOnly is the YYYY-MM-DD part of an RFC 3339 time, or "".
func dateOnly(t string) string {
	if len(t) < len(time.DateOnly) {
		return ""
	}
	return t[:len(time.DateOnly)]
}
