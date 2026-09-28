package ai

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testAuthContext(env map[string]string, files ...string) AuthContext {
	return AuthContext{
		Env: func(name string) (string, bool) {
			value := env[name]
			return value, strings.TrimSpace(value) != ""
		},
		FileExists: func(path string) bool {
			return slices.Contains(files, path)
		},
	}
}

func fakeOAuth(refreshes *atomic.Int32, refreshed Credential, err error) *OAuthAuth {
	return &OAuthAuth{
		Name: "fake",
		Refresh: func(_ context.Context, current Credential) (Credential, error) {
			refreshes.Add(1)
			if err != nil {
				return Credential{}, err
			}
			return refreshed, nil
		},
		ToAuth: func(credential Credential) (ModelAuth, error) {
			return ModelAuth{APIKey: credential.Access}, nil
		},
	}
}

func TestResolveProviderAuthStoredCredentialOwnsProvider(t *testing.T) {
	ctx := context.Background()
	env := testAuthContext(map[string]string{"OPENAI_API_KEY": "env-key"})
	auth := ProviderAuth{APIKey: EnvAPIKeyAuth("OpenAI API key", "OPENAI_API_KEY")}

	// A stored OAuth credential with no OAuth handler resolves nothing; the
	// environment key is not a silent fallback.
	stored := NewInMemoryAuthStorage(map[string]Credential{"openai": {Type: CredentialOAuth, Access: "a", Refresh: "r", Expires: 1}})
	result, err := ResolveProviderAuth(ctx, "openai", auth, stored, env, AuthResolutionOverrides{})
	if err != nil || result != nil {
		t.Fatalf("stored oauth without handler = %+v, %v; want nil", result, err)
	}

	empty := NewInMemoryAuthStorage(nil)
	result, err = ResolveProviderAuth(ctx, "openai", auth, empty, env, AuthResolutionOverrides{})
	if err != nil || result.Auth.APIKey != "env-key" || result.Source != "OPENAI_API_KEY" {
		t.Fatalf("ambient = %+v, %v", result, err)
	}

	override := "override-key"
	result, err = ResolveProviderAuth(ctx, "openai", auth, stored, env, AuthResolutionOverrides{APIKey: &override})
	if err != nil || result.Auth.APIKey != "override-key" || result.Source != "stored credential" {
		t.Fatalf("override = %+v, %v", result, err)
	}

	withKey := NewInMemoryAuthStorage(map[string]Credential{"openai": {Type: CredentialAPIKey, Key: "stored-key"}})
	result, err = ResolveProviderAuth(ctx, "openai", auth, withKey, env, AuthResolutionOverrides{})
	if err != nil || result.Auth.APIKey != "stored-key" || result.Source != "stored credential" {
		t.Fatalf("stored = %+v, %v", result, err)
	}
}

func TestResolveProviderAuthRefreshesExpiringOAuthOnceAndPersists(t *testing.T) {
	ctx := context.Background()
	var refreshes atomic.Int32
	future := time.Now().Add(time.Hour).UnixMilli()
	oauth := fakeOAuth(&refreshes, Credential{Type: CredentialOAuth, Access: "fresh", Refresh: "r2", Expires: future}, nil)
	store := NewInMemoryAuthStorage(map[string]Credential{"codex": {Type: CredentialOAuth, Access: "old", Refresh: "r", Expires: 0}})

	result, err := ResolveProviderAuth(ctx, "codex", ProviderAuth{OAuth: oauth}, store, testAuthContext(nil), AuthResolutionOverrides{})
	if err != nil || result.Auth.APIKey != "fresh" || result.Source != "OAuth" {
		t.Fatalf("resolve = %+v, %v", result, err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	persisted, _ := store.Read(ctx, "codex")
	if persisted.Access != "fresh" || persisted.Refresh != "r2" {
		t.Fatalf("persisted = %+v", persisted)
	}

	// A token valid for more than five minutes is used as stored.
	if _, err := ResolveProviderAuth(ctx, "codex", ProviderAuth{OAuth: oauth}, store, testAuthContext(nil), AuthResolutionOverrides{}); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d after a valid token, want 1", refreshes.Load())
	}
}

// racingStore simulates another process refreshing between the optimistic
// read and the locked re-check.
type racingStore struct {
	*InMemoryAuthStorage
	rotated Credential
}

func (s *racingStore) Modify(ctx context.Context, providerID string, fn func(*Credential) (*Credential, error)) (*Credential, error) {
	rotated := s.rotated
	next, err := fn(&rotated)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return &rotated, nil
	}
	return next, nil
}

func TestResolveProviderAuthRechecksExpiryUnderTheLock(t *testing.T) {
	var refreshes atomic.Int32
	store := &racingStore{
		InMemoryAuthStorage: NewInMemoryAuthStorage(map[string]Credential{"codex": {Type: CredentialOAuth, Access: "old", Refresh: "r", Expires: 0}}),
		rotated:             Credential{Type: CredentialOAuth, Access: "rotated", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}
	oauth := fakeOAuth(&refreshes, Credential{}, nil)
	result, err := ResolveProviderAuth(context.Background(), "codex", ProviderAuth{OAuth: oauth}, store, testAuthContext(nil), AuthResolutionOverrides{})
	if err != nil || result.Auth.APIKey != "rotated" {
		t.Fatalf("resolve = %+v, %v; want the credential another process rotated", result, err)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("refreshes = %d, want 0", refreshes.Load())
	}
}

func TestResolveProviderAuthReportsRefreshFailures(t *testing.T) {
	var refreshes atomic.Int32
	oauth := fakeOAuth(&refreshes, Credential{}, errors.New("invalid_grant"))
	store := NewInMemoryAuthStorage(map[string]Credential{"codex": {Type: CredentialOAuth, Access: "old", Refresh: "r", Expires: 0}})
	_, err := ResolveProviderAuth(context.Background(), "codex", ProviderAuth{OAuth: oauth, APIKey: EnvAPIKeyAuth("k", "CODEX_KEY")}, store,
		testAuthContext(map[string]string{"CODEX_KEY": "env"}), AuthResolutionOverrides{})
	if err == nil || err.Error() != "OAuth refresh failed for codex: invalid_grant" {
		t.Fatalf("refresh failure = %v; want no environment fallback", err)
	}
	persisted, _ := store.Read(context.Background(), "codex")
	if persisted.Access != "old" {
		t.Fatalf("failed refresh changed the stored credential: %+v", persisted)
	}
}

// InMemoryAuthStorage is a process-local CredentialStore for tests.
type InMemoryAuthStorage struct {
	mu          sync.Mutex
	credentials map[string]Credential
}

// NewInMemoryAuthStorage returns a store seeded with a copy of data.
func NewInMemoryAuthStorage(data map[string]Credential) *InMemoryAuthStorage {
	credentials := make(map[string]Credential, len(data))
	for providerID, credential := range data {
		credentials[providerID] = cloneCredential(credential)
	}
	return &InMemoryAuthStorage{credentials: credentials}
}

// Read implements CredentialStore.
func (s *InMemoryAuthStorage) Read(ctx context.Context, providerID string) (*Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	credential, ok := s.credentials[providerID]
	s.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return resolveStoredCredential(credential), nil
}

// List implements CredentialStore.
func (s *InMemoryAuthStorage) List(ctx context.Context) ([]CredentialInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return credentialInfos(s.credentials), nil
}

// Modify implements CredentialStore; calls are serialized.
func (s *InMemoryAuthStorage) Modify(ctx context.Context, providerID string, fn func(current *Credential) (*Credential, error)) (*Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var current *Credential
	if credential, ok := s.credentials[providerID]; ok {
		current = new(cloneCredential(credential))
	}
	next, err := fn(current)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if next == nil {
		return current, nil
	}
	s.credentials[providerID] = cloneCredential(*next)
	return new(cloneCredential(*next)), nil
}
