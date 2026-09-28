package ai

import (
	"slices"
	"testing"
)

// clearEnvKeyVars unsets every variable the table reads, so each case starts
// from an empty environment.
func clearEnvKeyVars(t *testing.T) {
	t.Helper()
	vertexADCCache.Lock()
	previous := vertexADCCache.exists
	vertexADCCache.exists = nil
	vertexADCCache.Unlock()
	t.Cleanup(func() {
		vertexADCCache.Lock()
		vertexADCCache.exists = previous
		vertexADCCache.Unlock()
	})
	names := []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", AnthropicAuthTokenEnv, AnthropicOAuthTokenEnv, AnthropicAPIKeyEnv,
		"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
		"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_BEARER_TOKEN_BEDROCK",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_WEB_IDENTITY_TOKEN_FILE"}
	for _, envVar := range envAPIKeyVars {
		names = append(names, envVar)
	}
	for _, name := range names {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", t.TempDir())
}

func TestEnvAPIKeysAnthropicAuthTokenIsReportedButNotAnAPIKey(t *testing.T) {
	clearEnvKeyVars(t)
	t.Setenv(AnthropicAuthTokenEnv, "auth-token")
	t.Setenv(AnthropicOAuthTokenEnv, "oauth-token")
	t.Setenv(AnthropicAPIKeyEnv, "api-key")
	if got := FindEnvKeys("anthropic", nil); !slices.Equal(got, []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}) {
		t.Fatalf("FindEnvKeys = %v", got)
	}
	if got := GetEnvAPIKey("anthropic", nil); got != "oauth-token" {
		t.Fatalf("GetEnvAPIKey = %q", got)
	}

	t.Setenv(AnthropicOAuthTokenEnv, "")
	t.Setenv(AnthropicAPIKeyEnv, "")
	if got := FindEnvKeys("anthropic", nil); !slices.Equal(got, []string{"ANTHROPIC_AUTH_TOKEN"}) {
		t.Fatalf("FindEnvKeys(auth token only) = %v", got)
	}
	if got := GetEnvAPIKey("anthropic", nil); got != "" {
		t.Fatalf("GetEnvAPIKey(auth token only) = %q", got)
	}

	t.Setenv(AnthropicAuthTokenEnv, "")
	t.Setenv(AnthropicOAuthTokenEnv, "oauth-token")
	if got := FindEnvKeys("anthropic", nil); !slices.Equal(got, []string{"ANTHROPIC_OAUTH_TOKEN"}) {
		t.Fatalf("FindEnvKeys(oauth token) = %v", got)
	}
	if got := GetEnvAPIKey("anthropic", nil); got != "oauth-token" {
		t.Fatalf("GetEnvAPIKey(oauth token) = %q", got)
	}

	t.Setenv(AnthropicOAuthTokenEnv, "")
	t.Setenv(AnthropicAPIKeyEnv, "api-key")
	if got := GetEnvAPIKey("anthropic", nil); got != "api-key" {
		t.Fatalf("GetEnvAPIKey(api key) = %q", got)
	}
}

func TestEnvAPIKeysUsedProviders(t *testing.T) {
	clearEnvKeyVars(t)
	for provider, envVar := range map[string]string{"openai": "OPENAI_API_KEY", "openrouter": "OPENROUTER_API_KEY"} {
		t.Setenv(envVar, provider+"-key")
		if got := GetEnvAPIKey(provider, nil); got != provider+"-key" {
			t.Errorf("%s: GetEnvAPIKey = %q", provider, got)
		}
		t.Setenv(envVar, "")
		if got := GetEnvAPIKey(provider, map[string]string{envVar: "scoped"}); got != "scoped" {
			t.Errorf("%s: scoped GetEnvAPIKey = %q", provider, got)
		}
	}
	if got := getAPIKeyEnvVars("openai-codex"); got != nil {
		t.Errorf("openai-codex env vars = %v, want none (OAuth only)", got)
	}
}
