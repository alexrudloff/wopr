package ai

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// builtInOAuthProviders is the built-in OAuth provider registry.
var builtInOAuthProviders = map[string]OAuthProviderInterface{
	"anthropic":      AnthropicOAuthProvider{},
	"github-copilot": copilotOAuthRegistryProvider{},
	"kimi-coding":    newKimiOAuthProvider(),
	"meta":           newMetaOAuthProvider(),
	"openai-codex":   CodexOAuthProvider{},
	"openrouter":     OpenRouterOAuthProvider{},
	"xai":            newXaiOAuthProvider(),
}

// copilotOAuthRegistryProvider adapts the GitHub Copilot device-flow helper to
// the shared OAuth provider registry used by model resolution and parity probes.
type copilotOAuthRegistryProvider struct{}

func (copilotOAuthRegistryProvider) ID() string               { return "github-copilot" }
func (copilotOAuthRegistryProvider) IsSubscription() bool     { return true }
func (copilotOAuthRegistryProvider) Name() string             { return "GitHub Copilot" }
func (copilotOAuthRegistryProvider) UsesCallbackServer() bool { return false }
func (copilotOAuthRegistryProvider) Login(ctx context.Context, callbacks OAuthLoginCallbacks) (OAuthCredentials, error) {
	cred, err := LoginGitHubCopilot(ctx, CopilotLoginCallbacks{
		OnPrompt: func(ctx context.Context) (string, error) {
			if callbacks.OnPrompt == nil {
				return "", nil
			}
			return callbacks.OnPrompt(OAuthPrompt{Message: "GitHub Enterprise URL/domain", Placeholder: "company.ghe.com", AllowEmpty: true})
		},
		OnAuth: func(url, userCode string) {
			if callbacks.OnDeviceCode != nil {
				callbacks.OnDeviceCode(OAuthDeviceCodeInfo{VerificationURI: url, UserCode: userCode})
				return
			}
			if callbacks.OnAuth != nil {
				instructions := userCode
				if strings.TrimSpace(userCode) != "" {
					instructions = "Enter code: " + userCode
				}
				callbacks.OnAuth(OAuthAuthInfo{URL: url, Instructions: instructions})
			}
		},
		OnProgress: callbacks.OnProgress,
	})
	if err != nil {
		return OAuthCredentials{}, err
	}
	return OAuthCredentials{Refresh: cred.Refresh, Access: cred.Access, Expires: cred.Expires}, nil
}
func (copilotOAuthRegistryProvider) RefreshToken(ctx context.Context, creds OAuthCredentials) (OAuthCredentials, error) {
	fresh, err := refreshCopilotToken(ctx, creds.Refresh, "")
	if err != nil {
		return OAuthCredentials{}, err
	}
	return OAuthCredentials{Refresh: fresh.Refresh, Access: fresh.Access, Expires: fresh.Expires}, nil
}
func (copilotOAuthRegistryProvider) GetAPIKey(creds OAuthCredentials) string { return creds.Access }

// GetOAuthProvider returns a built-in OAuth provider by id.
func GetOAuthProvider(id string) (OAuthProviderInterface, bool) {
	p, ok := builtInOAuthProviders[id]
	return p, ok
}

// GetOAuthProviders returns every built-in OAuth provider.
func GetOAuthProviders() []OAuthProviderInterface {
	return slices.Collect(maps.Values(builtInOAuthProviders))
}

// GetOAuthAPIKey resolves an OAuth-backed API key, refreshing the credential
// when expired.
func GetOAuthAPIKey(ctx context.Context, providerID string, credentials map[string]OAuthCredentials) (*OAuthCredentials, string, error) {
	provider, ok := GetOAuthProvider(providerID)
	if !ok {
		return nil, "", fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	creds, ok := credentials[providerID]
	if !ok {
		return nil, "", nil
	}

	if time.Now().UnixMilli() >= creds.Expires {
		refreshed, err := provider.RefreshToken(ctx, creds)
		if err != nil {
			return nil, "", fmt.Errorf("failed to refresh OAuth token for %s: %w", providerID, err)
		}
		creds = refreshed
	}

	return &creds, provider.GetAPIKey(creds), nil
}

// ResolveOAuthAPIKeyFromStorage loads a provider's OAuth credential from
// auth.json, refreshes it if needed, persists any refresh, and returns the
// runtime API key.
func ResolveOAuthAPIKeyFromStorage(ctx context.Context, storage *AuthStorage, providerID string) (string, error) {
	if storage == nil {
		return "", errors.New("nil auth storage")
	}
	credential, ok, err := storage.GetRaw(providerID)
	if err != nil || !ok || credential.Type != CredentialOAuth {
		return "", err
	}
	return resolveStoredOAuthAPIKey(ctx, storage, providerID, credential)
}

func resolveStoredOAuthAPIKey(ctx context.Context, storage *AuthStorage, providerID string, credential Credential) (string, error) {
	next, apiKey, err := GetOAuthAPIKey(ctx, providerID, map[string]OAuthCredentials{
		providerID: {
			Refresh:   credential.Refresh,
			Access:    credential.Access,
			Expires:   credential.Expires,
			ProjectID: credential.ProjectID,
		},
	})
	if err != nil {
		return "", err
	}
	if next == nil || apiKey == "" {
		return "", nil
	}
	if next.Refresh != credential.Refresh || next.Access != credential.Access || next.Expires != credential.Expires || next.ProjectID != credential.ProjectID {
		if err := storage.Set(providerID, Credential{
			Type:      CredentialOAuth,
			Refresh:   next.Refresh,
			Access:    next.Access,
			Expires:   next.Expires,
			ProjectID: next.ProjectID,
		}); err != nil {
			return "", err
		}
	}
	return apiKey, nil
}

// ResolveStoredAPIKeyFromStorage returns a refreshed OAuth key or a
// resolved api_key credential. A stored credential is checked before caller
// fallbacks, and OAuth refresh follows ctx.
func ResolveStoredAPIKeyFromStorage(ctx context.Context, storage *AuthStorage, providerID string) (key string, ok bool, err error) {
	if storage == nil {
		return "", false, errors.New("nil auth storage")
	}
	credential, found, err := storage.GetRaw(providerID)
	if err != nil || !found {
		return "", false, err
	}
	switch credential.Type {
	case CredentialOAuth:
		key, err = resolveStoredOAuthAPIKey(ctx, storage, providerID, credential)
	case CredentialAPIKey:
		key = resolveStoredCredential(credential).Key
	}
	return key, key != "", err
}
