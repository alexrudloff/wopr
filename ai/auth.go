package ai

// Stores per-provider credentials at <agentDir>/auth.json with mode 0600 and
// uses an OS-level file lock to keep concurrent instances from stomping on
// each other during token refresh.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/alexrudloff/wopr/internal/text"
)

// AuthSource reports where auth would be sourced from.
type AuthSource string

const (
	AuthSourceStored            AuthSource = "stored"
	AuthSourceRuntime           AuthSource = "runtime"
	AuthSourceEnvironment       AuthSource = "environment"
	AuthSourceFallback          AuthSource = "fallback"
	AuthSourceModelsJSONKey     AuthSource = "models_json_key"
	AuthSourceModelsJSONCommand AuthSource = "models_json_command"
)

// AuthStatus reports whether auth is configured and where wopr would source
// it from, without exposing secret values.
type AuthStatus struct {
	Configured bool
	Source     AuthSource
	Label      string
}

// CredentialType is the discriminator stored as `"type"` in auth.json.
//
// Values:
//   - "api_key" for raw API keys (field: "key")
//   - "oauth"   for OAuth refresh/access tokens
//
// Earlier WOPR builds wrote "type":"api" / "apiKey". The Load() path
// migrates the old shape forward in place on first read.
type CredentialType string

const (
	CredentialAPIKey CredentialType = "api_key"
	CredentialOAuth  CredentialType = "oauth"

	// legacyCredentialAPIKey is the legacy discriminator. Detected
	// during Load() and rewritten to CredentialAPIKey on next write.
	legacyCredentialAPIKey CredentialType = "api"
)

// Credential is the on-disk shape for one provider's credentials.
// We use a flat struct that covers both the api-key and oauth variants -
// fields not relevant to the type are simply zero-valued.
type Credential struct {
	Type CredentialType `json:"type"`

	// API-key variant. Field is "key"; we keep an
	// alias "apiKey" on read for backwards compatibility with auth.json
	// files written by older wopr builds.
	Key string `json:"key,omitempty"`

	// Env holds provider-scoped environment overrides for an API-key
	// credential. Values take precedence over the process environment when
	// resolving this credential's own `$VAR` references and when the provider
	// reads configuration env.
	Env map[string]string `json:"env,omitempty"`

	// OAuth variant (used by github-copilot, anthropic, etc.)
	Refresh          string `json:"refresh,omitempty"`
	Access           string `json:"access,omitempty"`
	Expires          int64  `json:"expires,omitempty"` // ms since epoch
	ProjectID        string `json:"projectId,omitempty"`
	EnterpriseDomain string `json:"enterpriseUrl,omitempty"`
}

// rawCredential mirrors Credential but also accepts the legacy `apiKey`
// JSON field. Used only during Load() to migrate old files in place.
type rawCredential struct {
	Type             CredentialType    `json:"type"`
	Key              string            `json:"key,omitempty"`
	LegacyAPIKey     string            `json:"apiKey,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Refresh          string            `json:"refresh,omitempty"`
	Access           string            `json:"access,omitempty"`
	Expires          int64             `json:"expires,omitempty"`
	ProjectID        string            `json:"projectId,omitempty"`
	EnterpriseDomain string            `json:"enterpriseUrl,omitempty"`
}

func (r rawCredential) normalize() Credential {
	c := Credential{
		Type:             r.Type,
		Key:              r.Key,
		Env:              r.Env,
		Refresh:          r.Refresh,
		Access:           r.Access,
		Expires:          r.Expires,
		ProjectID:        r.ProjectID,
		EnterpriseDomain: r.EnterpriseDomain,
	}
	if c.Type == legacyCredentialAPIKey {
		c.Type = CredentialAPIKey
	}
	if c.Key == "" && r.LegacyAPIKey != "" {
		c.Key = r.LegacyAPIKey
	}
	return c
}

// AuthStorage is the persistent credential store at <agentDir>/auth.json.
type AuthStorage struct {
	path string
	mu   sync.Mutex // process-local guard around the file lock and read state
	read authReadState
}

// authReadState is the parsed auth.json snapshot and the file revision it was
// read at. Reads reuse the
// snapshot while the revision is unchanged, so repeated credential lookups do
// not reread and reparse the file.
type authReadState struct {
	revision string
	creds    map[string]Credential
}

// NewAuthStorage opens (or creates) the credential store at the given path.
// The parent directory is created with mode 0700 if it does not exist.
func NewAuthStorage(path string) (*AuthStorage, error) {
	if path == "" {
		return nil, errors.New("auth: empty path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: ensure dir: %w", err)
	}
	return &AuthStorage{path: path}, nil
}

// Path returns the auth.json file path.
func (a *AuthStorage) Path() string { return a.path }

// Load reads all credentials. Returns an empty map if the file does not exist.
func (a *AuthStorage) Load() (map[string]Credential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	creds, err := a.readLatestLocked()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Credential, len(creds))
	for provider, cred := range creds {
		out[provider] = cred.clone()
	}
	return out, nil
}

// credential returns one provider's stored credential from the latest snapshot.
func (a *AuthStorage) credential(provider string) (Credential, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	creds, err := a.readLatestLocked()
	if err != nil {
		return Credential{}, false, err
	}
	cred, ok := creds[provider]
	return cred.clone(), ok, nil
}

// readLatestLocked returns the cached snapshot when auth.json still has the
// revision it was read at, and rereads the file otherwise. The revision is taken before the read, so a
// write that lands during the read is detected by the next call.
func (a *AuthStorage) readLatestLocked() (map[string]Credential, error) {
	revision, ok := authFileRevision(a.path)
	if ok && a.read.creds != nil && revision == a.read.revision {
		return a.read.creds, nil
	}
	creds, err := a.loadLocked()
	if err != nil {
		a.read = authReadState{}
		return nil, err
	}
	if ok {
		a.read = authReadState{revision: revision, creds: creds}
	} else {
		a.read = authReadState{}
	}
	return creds, nil
}

func (c Credential) clone() Credential {
	c.Env = maps.Clone(c.Env)
	return c
}

// readAuthFile reads auth.json. Tests replace it to count file reads.
var readAuthFile = os.ReadFile

func (a *AuthStorage) loadLocked() (map[string]Credential, error) {
	data, err := readAuthFile(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Credential{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth: read: %w", err)
	}
	if len(data) == 0 {
		return map[string]Credential{}, nil
	}
	raw := map[string]rawCredential{}
	if err := json.Unmarshal(text.StripBomBytes(data), &raw); err != nil {
		return nil, fmt.Errorf("auth: parse: %w", err)
	}
	// Legacy shapes are normalized in memory only: reads never write, and the next write stores the current shape.
	creds := make(map[string]Credential, len(raw))
	for k, r := range raw {
		creds[k] = r.normalize()
	}
	return creds, nil
}

// Get is Read without a context: the credential for a provider, if present,
// with an API key resolved as a configuration value ("$OPENAI_API_KEY",
// "!op read ..."); the on-disk value is left untouched.
func (a *AuthStorage) Get(provider string) (Credential, bool, error) {
	c, err := a.Read(context.Background(), provider)
	if err != nil || c == nil {
		return Credential{}, false, err
	}
	return *c, true, nil
}

// GetProviderEnv returns a copy of the provider-scoped environment overrides
// for an API-key credential, or nil when none are set.
func (a *AuthStorage) GetProviderEnv(provider string) (map[string]string, error) {
	c, ok, err := a.credential(provider)
	if err != nil {
		return nil, err
	}
	if !ok || c.Type != CredentialAPIKey || len(c.Env) == 0 {
		return nil, nil
	}
	return c.Env, nil
}

// GetRaw returns the credential without applying configvalue.Resolve to
// the Key field. Use this when you need the on-disk value (e.g. for
// rewriting auth.json or for OAuth flows where Refresh/Access tokens
// must round-trip verbatim).
func (a *AuthStorage) GetRaw(provider string) (Credential, bool, error) {
	return a.credential(provider)
}

// GetAuthStatus reports whether auth is configured for provider without
// exposing credential values or refreshing OAuth tokens.
func (a *AuthStorage) GetAuthStatus(provider string) AuthStatus {
	if _, ok, err := a.GetRaw(provider); err == nil && ok {
		return AuthStatus{Configured: true, Source: AuthSourceStored}
	}
	if key := firstAuthEnvKey(provider); key != "" {
		return AuthStatus{Configured: false, Source: AuthSourceEnvironment, Label: key}
	}
	return AuthStatus{Configured: false}
}

// Set writes (or replaces) the credential for a provider through Modify.
func (a *AuthStorage) Set(provider string, cred Credential) error {
	_, err := a.Modify(context.Background(), provider, func(*Credential) (*Credential, error) { return &cred, nil })
	return err
}

// Delete removes the credential for a provider (no-op if absent).
func (a *AuthStorage) Delete(provider string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withFileLock(func() error {
		creds, err := a.loadLocked()
		if err != nil {
			return err
		}
		delete(creds, provider)
		return a.writeLocked(creds)
	})
}

func (a *AuthStorage) writeLocked(creds map[string]Credential) error {
	a.read = authReadState{}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: marshal: %w", err)
	}
	// Atomic write: temp file + rename, then chmod 0600.
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".auth-*.json")
	if err != nil {
		return fmt.Errorf("auth: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("auth: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("auth: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("auth: close tmp: %w", err)
	}
	if err := os.Rename(tmpName, a.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("auth: rename: %w", err)
	}
	return nil
}

// withFileLock acquires an exclusive lock on a sidecar lockfile, runs fn,
// then releases. We lock a sidecar (not auth.json itself) so the rename
// during writeLocked doesn't invalidate the lock on the inode.
func (a *AuthStorage) withFileLock(fn func() error) error {
	return withSidecarLock(a.path, "auth: flock", errors.New("auth: timed out acquiring auth.json lock"), fn)
}

// withSidecarLock holds an exclusive lock on path+".lock" while fn runs,
// retrying every 20ms for up to 2s. Lock errors are prefixed with errPrefix;
// running out of time returns timeoutErr.
func withSidecarLock(path, errPrefix string, timeoutErr error, fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lock := flock.New(path + ".lock")
	if _, err := lock.TryLockContext(ctx, 20*time.Millisecond); errors.Is(err, context.DeadlineExceeded) {
		return timeoutErr
	} else if err != nil {
		return fmt.Errorf("%s: %w", errPrefix, err)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func firstAuthEnvKey(provider string) string {
	if keys := FindEnvKeys(provider, nil); len(keys) > 0 {
		return keys[0]
	}
	return ""
}
