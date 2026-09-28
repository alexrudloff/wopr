package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestAuthLegacyMigration verifies an auth.json written by an older wopr
// build (or hand-edited) with "type":"api"/"apiKey" reads in the current
// shape without being rewritten on read, and
// that the next write stores the current shape.
func TestAuthLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	legacy := []byte(`{"openai":{"type":"api","apiKey":"sk-old"}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store, err := NewAuthStorage(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	cred, ok, err := store.Get("openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatalf("missing openai entry")
	}
	if cred.Type != CredentialAPIKey {
		t.Errorf("type: want %q got %q", CredentialAPIKey, cred.Type)
	}
	if cred.Key != "sk-old" {
		t.Errorf("key: want sk-old got %q", cred.Key)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != string(legacy) {
		t.Fatalf("read rewrote auth.json: %s, %v", raw, err)
	}

	// The next write stores every entry in the current format.
	if err := store.Set("anthropic", Credential{Type: CredentialAPIKey, Key: "sk-new"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	var disk map[string]map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := disk["openai"]
	if got["type"] != "api_key" {
		t.Errorf("post-write type: want api_key got %v", got["type"])
	}
	if got["key"] != "sk-old" {
		t.Errorf("post-write key: want sk-old got %v", got["key"])
	}
	if _, hasOld := got["apiKey"]; hasOld {
		t.Errorf("post-write must drop legacy apiKey field, got %v", got)
	}
}

// TestAuthOAuthRoundTrip ensures the oauth variant is unchanged by the
// 1.7 fix.
func TestAuthOAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAuthStorage(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := Credential{
		Type:             CredentialOAuth,
		Refresh:          "rt",
		Access:           "at",
		Expires:          1234567890,
		EnterpriseDomain: "github.example.com",
	}
	if err := store.Set("github-copilot", in); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok, err := store.Get("github-copilot")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, in)
	}
}

// TestAuthGetResolvesAPIKeyFromEnv proves the wiring: an auth.json where
// the api_key value is an explicit "$ENV_VAR" reference resolves to the env
// value when read via Get. Bare names are literals and env references require
// the "$" sigil (the startup migration rewrites legacy bare names to "$NAME").
func TestAuthGetResolvesAPIKeyFromEnv(t *testing.T) {
	t.Setenv("WOPR_TEST_OPENAI_KEY_VIA_ENV", "sk-resolved-from-env")

	dir := t.TempDir()
	store, err := NewAuthStorage(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Store an explicit env reference: {"openai":{"type":"api_key","key":"$OPENAI_API_KEY"}}.
	if err := store.Set("openai", Credential{Type: CredentialAPIKey, Key: "$WOPR_TEST_OPENAI_KEY_VIA_ENV"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	cred, ok, err := store.Get("openai")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if cred.Key != "sk-resolved-from-env" {
		t.Errorf("Get should resolve env-var-name; got Key=%q want sk-resolved-from-env", cred.Key)
	}

	// On-disk value is preserved verbatim (Set wrote the literal name).
	raw, _, err := store.GetRaw("openai")
	if err != nil {
		t.Fatalf("getraw: %v", err)
	}
	if raw.Key != "$WOPR_TEST_OPENAI_KEY_VIA_ENV" {
		t.Errorf("GetRaw should return on-disk value; got Key=%q", raw.Key)
	}
}

// TestAuthFileWithBOMLoads checks that a hand-edited auth.json saved with a byte
// order mark still loads.
func TestAuthFileWithBOMLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf"+`{"openai":{"type":"api_key","key":"sk-bom"}}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store, err := NewAuthStorage(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	cred, ok, err := store.Get("openai")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if cred.Key != "sk-bom" {
		t.Fatalf("key = %q, want sk-bom", cred.Key)
	}
}
