package ai

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type fakeOAuthProvider struct{}

func (fakeOAuthProvider) ID() string               { return "fake-oauth" }
func (fakeOAuthProvider) Name() string             { return "Fake OAuth" }
func (fakeOAuthProvider) UsesCallbackServer() bool { return false }
func (fakeOAuthProvider) Login(context.Context, OAuthLoginCallbacks) (OAuthCredentials, error) {
	return OAuthCredentials{}, nil
}
func (fakeOAuthProvider) RefreshToken(context.Context, OAuthCredentials) (OAuthCredentials, error) {
	return OAuthCredentials{Refresh: "new-refresh", Access: "new-access", Expires: time.Now().Add(time.Hour).UnixMilli()}, nil
}
func (fakeOAuthProvider) GetAPIKey(creds OAuthCredentials) string { return creds.Access }

func TestGetOAuthAPIKey_AnthropicPassThrough(t *testing.T) {
	next, apiKey, err := GetOAuthAPIKey(context.Background(), "anthropic", map[string]OAuthCredentials{
		"anthropic": {Access: "access-token", Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli()},
	})
	if err != nil {
		t.Fatalf("GetOAuthAPIKey: %v", err)
	}
	if next == nil {
		t.Fatal("next = nil")
	}
	if apiKey != "access-token" {
		t.Fatalf("apiKey = %q, want access-token", apiKey)
	}
}

func TestResolveOAuthAPIKeyFromStorage_PersistsRefresh(t *testing.T) {
	old := builtInOAuthProviders["fake-oauth"]
	builtInOAuthProviders["fake-oauth"] = fakeOAuthProvider{}
	defer func() {
		if old != nil {
			builtInOAuthProviders["fake-oauth"] = old
		} else {
			delete(builtInOAuthProviders, "fake-oauth")
		}
	}()

	store, err := NewAuthStorage(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("fake-oauth", Credential{
		Type:    CredentialOAuth,
		Refresh: "old-refresh",
		Access:  "old-access",
		Expires: time.Now().Add(-time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	apiKey, err := ResolveOAuthAPIKeyFromStorage(context.Background(), store, "fake-oauth")
	if err != nil {
		t.Fatalf("ResolveOAuthAPIKeyFromStorage: %v", err)
	}
	if apiKey != "new-access" {
		t.Fatalf("apiKey = %q, want new-access", apiKey)
	}
	cred, ok, err := store.GetRaw("fake-oauth")
	if err != nil || !ok {
		t.Fatalf("GetRaw = (%v,%v)", ok, err)
	}
	if cred.Access != "new-access" || cred.Refresh != "new-refresh" {
		t.Fatalf("stored cred = %+v", cred)
	}
}

func TestCodexOAuthProvider_GetAPIKeyAccessToken(t *testing.T) {
	p, ok := GetOAuthProvider("openai-codex")
	if !ok {
		t.Fatal("missing openai-codex provider")
	}
	got := p.GetAPIKey(OAuthCredentials{Access: "access.jwt"})
	if got != "access.jwt" {
		t.Fatalf("GetAPIKey = %q", got)
	}
}
