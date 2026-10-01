package codingagent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/alexrudloff/wopr/ai"
)

// A connection's model list comes from the provider itself, so a model
// released yesterday shows up today: Anthropic's, Google's, and ChatGPT's
// model lists, and /models on OpenAI-compatible APIs. models.dev fills in
// what a list leaves out (context window, prices, thinking levels), and the
// list is kept so every model setup offers is one wopr can run.

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
func (w *setupWizard) liveModels(ctx context.Context, provider string) ([]ai.LiveModel, error) {
	key, oauth := w.providerCredential(ctx, provider)
	return fetchLiveModels(ctx, provider, key, oauth)
}

// providerModels is a connection's models for its checklist: the
// provider's own list, with the models in use it no longer lists kept so
// they can be unchecked. note explains when the list came from models.dev
// instead.
func (w *setupWizard) providerModels(provider, providerName string, inUse []string) (items []remoteModel, note string) {
	var fetched []ai.LiveModel
	var listErr error
	progress := &setupProgress{title: "Getting the model list from " + providerName, rows: []progressRow{{label: "asking " + providerName + " which models it has", state: rowRunning}}, autoClose: true}
	w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
		models, err := w.liveModels(ctx, provider)
		update(func() { fetched, listErr = models, err })
	})
	if progress.stopped && listErr == nil && fetched == nil {
		listErr = errors.New("stopped")
	}
	if listErr == nil && len(fetched) > 0 {
		StoreLiveModels(w.m.opts.AgentDir, provider, fetched)
	}
	live := liveToRemote(fetched)
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
		// The provider's list may leave out what models.dev knows.
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
