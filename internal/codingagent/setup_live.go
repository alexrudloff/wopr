package codingagent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// A connection's model list comes from the provider itself, so a model
// released yesterday shows up today: Anthropic's and Google's model lists,
// and /models on OpenAI-compatible APIs. wopr's catalog only fills in what
// a provider's list leaves out (context window, prices, thinking levels).

// errNoModelList reports a provider whose API has no model list wopr can
// read.
var errNoModelList = errors.New("this provider doesn't publish a model list")

// providerAPI is the API and base URL wopr uses for a provider, from its
// catalog entries: an OpenAI-compatible base when it has one.
func providerAPI(provider string) (ai.API, string) {
	var api ai.API
	base := ""
	for _, m := range ai.CatalogModels {
		if m.Provider != provider || m.BaseURL == "" || strings.Contains(m.BaseURL, "{") {
			continue
		}
		if base == "" || m.API == ai.APIOpenAICompletions && api != ai.APIOpenAICompletions {
			api, base = m.API, strings.TrimRight(m.BaseURL, "/")
		}
	}
	return api, base
}

// providerCredential is a provider's key or sign-in token, and whether it
// is a sign-in (OAuth) token.
func (w *setupWizard) providerCredential(ctx context.Context, provider string) (string, bool) {
	if auth, err := ai.NewAuthStorage(filepath.Join(w.m.opts.AgentDir, "auth.json")); err == nil {
		if cred, ok, _ := auth.GetRaw(provider); ok {
			if key, found, err := ai.ResolveStoredAPIKeyFromStorage(ctx, auth, provider); err == nil && found {
				return key, cred.Type == ai.CredentialOAuth
			}
		}
	}
	return ai.GetEnvAPIKey(provider, nil), false
}

// liveModels asks a provider for its models.
func (w *setupWizard) liveModels(ctx context.Context, provider string) ([]remoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api, base := providerAPI(provider)
	key, oauth := w.providerCredential(ctx, provider)
	switch api {
	case ai.APIAnthropicMessages:
		return anthropicModels(ctx, strings.TrimSuffix(base, "/v1"), key, oauth)
	case ai.APIGoogleGenerativeAI:
		return googleModels(ctx, base, key)
	case ai.APIOpenAICompletions, ai.APIOpenAIResponses, ai.APIMistralConversations:
		models, _, err := listEndpointModels(ctx, setupEndpoint{BaseURL: base, APIKey: key})
		if err != nil {
			return nil, err
		}
		chat := models[:0]
		for _, m := range models {
			if chatModel(m.ID) {
				chat = append(chat, m)
			}
		}
		return chat, nil
	}
	return nil, errNoModelList
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
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// anthropicModels reads Anthropic's model list, page by page, newest first.
// A subscription's sign-in token is sent as a bearer token with the OAuth
// beta header; an API key as x-api-key.
func anthropicModels(ctx context.Context, base, key string, oauth bool) ([]remoteModel, error) {
	header := http.Header{}
	header.Set("anthropic-version", "2023-06-01")
	if oauth {
		header.Set("Authorization", "Bearer "+key)
		header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		header.Set("x-api-key", key)
	}
	var out []remoteModel
	after := ""
	for range 20 {
		query := url.Values{"limit": {"1000"}}
		if after != "" {
			query.Set("after_id", after)
		}
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := getJSON(ctx, base+"/v1/models?"+query.Encode(), header, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			out = append(out, remoteModel{ID: m.ID, Name: m.DisplayName})
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
func googleModels(ctx context.Context, base, key string) ([]remoteModel, error) {
	var out []remoteModel
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
			generates := false
			for _, method := range m.Methods {
				generates = generates || method == "generateContent"
			}
			if generates {
				out = append(out, remoteModel{ID: strings.TrimPrefix(m.Name, "models/"), Name: m.DisplayName, Context: m.InputTokenLimit, MaxOutput: m.OutputTokenLimit})
			}
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	return out, nil
}

// providerModels is a connection's models for its checklist: the
// provider's own list, with the models in use it no longer lists kept so
// they can be unchecked. note explains when the list came from wopr's
// catalog instead.
func (w *setupWizard) providerModels(provider, providerName string, inUse []string) (items []remoteModel, note string) {
	var live []remoteModel
	var listErr error
	progress := &setupProgress{title: "Getting the model list from " + providerName, rows: []progressRow{{label: "asking " + providerName + " which models it has", state: rowRunning}}, autoClose: true}
	w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
		models, err := w.liveModels(ctx, provider)
		update(func() { live, listErr = models, err })
	})
	if progress.stopped && listErr == nil && live == nil {
		listErr = errors.New("stopped")
	}
	if listErr != nil || len(live) == 0 {
		// The provider couldn't be asked: wopr's known models, said plainly.
		for _, m := range ai.ListModels(provider) {
			live = append(live, remoteModel{ID: m.ID, Name: m.DisplayName, Context: m.ContextWindow, MaxOutput: m.MaxOutputTokens})
		}
		reason := "it listed no models"
		if listErr != nil {
			reason = listErr.Error()
		}
		note = fmt.Sprintf("Couldn't get the list from %s (%s); these are the models wopr knows.", providerName, reason)
	}
	listed := map[string]bool{}
	for i, m := range live {
		listed[m.ID] = true
		// The provider's list may leave out what the catalog knows.
		if cat, ok := ai.LookupModelExact(provider + "/" + m.ID); ok {
			live[i].Name = cmp.Or(m.Name, cat.DisplayName)
			live[i].Context = cmp.Or(m.Context, cat.ContextWindow)
			live[i].MaxOutput = cmp.Or(m.MaxOutput, cat.MaxOutputTokens)
		}
	}
	for _, id := range inUse {
		if !listed[id] {
			live = append(live, remoteModel{ID: id, Name: w.m.modelName(provider, id) + " · not in " + providerName + "'s list"})
		}
	}
	return live, note
}
