package main

// Credential commands: secrets are printed only in the form asked for, and
// read-only checks never create files or refresh tokens.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// isolateAuthEnv clears the ambient credentials the built-in providers read so
// a developer's environment cannot satisfy a check.
func isolateAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		t.Setenv(name, "")
	}
}

func TestLoginListJSONIsSecretFree(t *testing.T) {
	t.Setenv("WOPR_HOME", t.TempDir())
	t.Setenv("WOPR_CODING_AGENT_DIR", t.TempDir())
	stdout, stderr, code := captureStdoutStderr(t, func() int {
		return runLoginCommand([]string{"login", "--list", "--json", "--no-input"})
	})
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var output authTargetListOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil || len(output.Targets) == 0 {
		t.Fatalf("output = %s, %v", stdout, err)
	}
	if strings.Contains(strings.ToLower(stdout), "token") || strings.Contains(strings.ToLower(stdout), "secret") {
		t.Fatalf("target inventory contains credential-shaped data: %s", stdout)
	}
}

func TestGetAuthCredentialExtractsOnlyBearerTokens(t *testing.T) {
	cases := []struct {
		name string
		auth *ai.AuthResult
		want string
	}{
		{"nil", nil, ""},
		{"api key wins", &ai.AuthResult{Auth: ai.ModelAuth{APIKey: "key", Headers: ai.ProviderHeaders{"Authorization": new("Bearer tok")}}}, "key"},
		{"bearer header", &ai.AuthResult{Auth: ai.ModelAuth{Headers: ai.ProviderHeaders{"authorization": new("bearer  tok")}}}, "tok"},
		{"basic header", &ai.AuthResult{Auth: ai.ModelAuth{Headers: ai.ProviderHeaders{"Authorization": new("Basic abc")}}}, ""},
		{"deleted header", &ai.AuthResult{Auth: ai.ModelAuth{Headers: ai.ProviderHeaders{"Authorization": nil}}}, ""},
		{"multi-line value", &ai.AuthResult{Auth: ai.ModelAuth{Headers: ai.ProviderHeaders{"Authorization": new("Bearer a\nb")}}}, ""},
		{"other header", &ai.AuthResult{Auth: ai.ModelAuth{Headers: ai.ProviderHeaders{"cf-aig-authorization": new("Bearer cf")}}}, ""},
	}
	for _, tc := range cases {
		if got := GetAuthCredential(tc.auth); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func writeAuthJSON(t *testing.T, agentDir string, content map[string]ai.Credential) {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runAuthForTest(t *testing.T, agentDir string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runAuthCommandWith(append([]string{"auth"}, args...), authRunEnv{stdout: &stdout, stderr: &stderr, agentDir: agentDir})
	return code, stdout.String(), stderr.String()
}

func TestAuthCommandPrintsSecretsOnlyInTheRequestedForm(t *testing.T) {
	isolateAuthEnv(t)
	agentDir := t.TempDir()
	const apiKey = "sk-test-openai-secret"
	const token = "codex-access-secret"
	writeAuthJSON(t, agentDir, map[string]ai.Credential{
		"openai":       {Type: ai.CredentialAPIKey, Key: apiKey},
		"openai-codex": {Type: ai.CredentialOAuth, Access: token, Refresh: "refresh-secret", Expires: time.Now().Add(2 * time.Hour).UnixMilli()},
	})
	noSecret := func(name string, outputs ...string) {
		t.Helper()
		for _, output := range outputs {
			for _, secret := range []string{apiKey, token, "refresh-secret"} {
				if strings.Contains(output, secret) {
					t.Fatalf("%s leaked %q in %q", name, secret, output)
				}
			}
		}
	}

	code, stdout, stderr := runAuthForTest(t, agentDir, "check", "--provider", "openai")
	if code != 0 || stdout != "ready\n" {
		t.Fatalf("check = %d %q %q", code, stdout, stderr)
	}
	noSecret("check", stdout, stderr)

	code, stdout, stderr = runAuthForTest(t, agentDir, "check", "--provider", "openai", "--json")
	if code != 0 || stdout != `{"status":"ready","provider":"openai","authType":"api_key"}`+"\n" {
		t.Fatalf("check --json = %d %q %q", code, stdout, stderr)
	}
	noSecret("check --json", stdout, stderr)

	code, stdout, _ = runAuthForTest(t, agentDir, "check", "--provider", "openai", "--credentials")
	if code != 0 || stdout != apiKey+"\n" {
		t.Fatalf("check --credentials = %d %q", code, stdout)
	}
	code, stdout, _ = runAuthForTest(t, agentDir, "check", "--provider", "openai-codex", "--json", "--credentials")
	if code != 0 || stdout != `{"status":"ready","provider":"openai-codex","authType":"oauth","credentials":"`+token+`"}`+"\n" {
		t.Fatalf("check --json --credentials = %d %q", code, stdout)
	}

	code, stdout, stderr = runAuthForTest(t, agentDir, "print-api-key", "--provider", "openai-codex")
	if code != 1 || stdout != "" || stderr != `Error: Provider "openai-codex" is configured with OAuth, not an API key`+"\n" {
		t.Fatalf("print-api-key oauth = %d %q %q", code, stdout, stderr)
	}
	noSecret("print-api-key oauth", stdout, stderr)

	code, stdout, stderr = runAuthForTest(t, agentDir, "print-bearer-token", "--provider", "openai")
	if code != 1 || stdout != "" || stderr != `Error: Provider "openai" is not configured with an OAuth bearer token`+"\n" {
		t.Fatalf("print-bearer-token api key = %d %q %q", code, stdout, stderr)
	}
	noSecret("print-bearer-token api key", stdout, stderr)

	// Both stored providers carry gpt-5.5; each print command selects only
	// its own credential type.
	code, stdout, stderr = runAuthForTest(t, agentDir, "print-api-key", "--model", "gpt-5.5")
	if code != 0 || stdout != apiKey+"\n" {
		t.Fatalf("print-api-key --model = %d %q %q", code, stdout, stderr)
	}
	code, stdout, stderr = runAuthForTest(t, agentDir, "print-bearer-token", "--model", "gpt-5.5")
	if code != 0 || stdout != token+"\n" {
		t.Fatalf("print-bearer-token --model = %d %q %q", code, stdout, stderr)
	}
}

func TestAuthCheckNoRefreshCreatesNothingAndDoesNotRefresh(t *testing.T) {
	isolateAuthEnv(t)
	agentDir := filepath.Join(t.TempDir(), "agent")
	code, stdout, _ := runAuthForTest(t, agentDir, "check", "--provider", "openai", "--no-refresh")
	if code != 1 || stdout != "not_ready\n" {
		t.Fatalf("check --no-refresh = %d %q", code, stdout)
	}
	if _, err := os.Stat(agentDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--no-refresh created the agent dir: %v", err)
	}

	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAuthJSON(t, agentDir, map[string]ai.Credential{"openai-codex": {Type: ai.CredentialOAuth, Access: "stale-token", Refresh: "r", Expires: 1}})
	code, stdout, _ = runAuthForTest(t, agentDir, "check", "--provider", "openai-codex", "--no-refresh", "--credentials")
	if code != 0 || stdout != "stale-token\n" {
		t.Fatalf("check --no-refresh --credentials = %d %q", code, stdout)
	}
}
