package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAuthenticatedProviders covers the default-routed providers: env keys
// and OAuth logins count as auth, an OAuth entry without a refresh token does
// not, and an API key never marks the OAuth-only openai-codex as usable.
func TestAuthenticatedProviders(t *testing.T) {
	oauth := func(refresh string) map[string]any {
		return map[string]any{"type": "oauth", "refresh": refresh, "access": "atok",
			"expires": time.Now().Add(24 * time.Hour).UnixMilli()}
	}
	cases := []struct {
		name    string
		env     map[string]string
		auth    map[string]any
		want    string
		present bool
	}{
		{"no auth", nil, nil, "anthropic", false},
		{"anthropic env", map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}, nil, "anthropic", true},
		{"openai key is not codex", map[string]string{"OPENAI_API_KEY": "sk"}, nil, "openai-codex", false},
		{"codex oauth", nil, map[string]any{"openai-codex": oauth("rtok")}, "openai-codex", true},
		{"oauth without refresh", nil, map[string]any{"openai-codex": oauth("")}, "openai-codex", false},
		{"ollama host", map[string]string{"OLLAMA_HOST": "http://localhost:11434"}, nil, "ollama", true},
		{"opencode-go api key in auth.json", nil, map[string]any{"opencode-go": map[string]any{"type": "api_key", "key": "sk-x"}}, "opencode-go", true},
		{"opencode-go key authenticates Zen too", nil, map[string]any{"opencode-go": map[string]any{"type": "api_key", "key": "sk-x"}}, "opencode", true},
		{"Zen key authenticates opencode-go too", nil, map[string]any{"opencode": map[string]any{"type": "api_key", "key": "sk-x"}}, "opencode-go", true},
		{"opencode-go oauth without refresh", nil, map[string]any{"opencode-go": oauth("")}, "opencode-go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "OPENROUTER_API_KEY", "OLLAMA_HOST", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY"} {
				t.Setenv(v, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			if tc.auth != nil {
				b, _ := json.Marshal(tc.auth)
				if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := AuthenticatedProviders(dir)[tc.want]; got != tc.present {
				t.Fatalf("AuthenticatedProviders()[%q] = %v, want %v", tc.want, got, tc.present)
			}
		})
	}
}

// TestReachableProvidersIncludesOpenCode guards the picker scope: opencode-go
// and opencode reach a real client in BuildModel, so a hard-coded set that drops
// them hides their models from the picker even when auth.json holds a key.
func TestReachableProvidersIncludesOpenCode(t *testing.T) {
	reachable := ReachableProviders()
	for _, p := range []string{"opencode", "opencode-go"} {
		if !reachable[p] {
			t.Errorf("ReachableProviders() missing %q", p)
		}
	}
}
