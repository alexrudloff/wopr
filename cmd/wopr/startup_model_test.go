package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

func isolateProviderAuthEnv(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasSuffix(name, "_API_KEY") || strings.HasSuffix(name, "_TOKEN") || strings.HasPrefix(name, "AWS_") ||
			strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "AZURE_") {
			t.Setenv(name, "")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	t.Setenv(codingagent.ENV_AGENT_DIR, dir)
	return dir
}

// TestSelectStartupModel drives startup model selection over the real
// catalog: an env key picks its provider's default, and a saved default
// without auth falls back instead of failing the first request.
func TestSelectStartupModel(t *testing.T) {
	for _, tc := range []struct {
		name         string
		options      startupModelOptions
		settings     codingagent.Settings
		want         string
		thinking     string
		errContains  string
		warnContains string
		api          ai.API
	}{
		{name: "built-in env key picks the provider default", want: "anthropic/claude-opus-4-8"},
		{name: "bare --model with a thinking suffix", options: startupModelOptions{CLIModel: "anthropic/claude-sonnet-4-5:high"}, want: "anthropic/claude-sonnet-4-5", thinking: "high"},
		{name: "unknown bare --model is an error", options: startupModelOptions{CLIModel: "no-such-model-xyz"}, errContains: `Model "no-such-model-xyz" not found`},
		{name: "saved default without auth falls back", settings: codingagent.Settings{DefaultProvider: "openai", DefaultModel: "gpt-4o-mini"}, want: "anthropic/claude-opus-4-8"},
		{name: "bedrock uses the Converse API", options: startupModelOptions{CLIModel: "amazon-bedrock/us.anthropic.claude-opus-4-6-v1"}, want: "amazon-bedrock/us.anthropic.claude-opus-4-6-v1", api: ai.APIBedrockConverseStream},
		{name: "unknown id under a known provider warns", options: startupModelOptions{CLIModel: "anthropic/claude-custom-x"}, want: "anthropic/claude-custom-x", warnContains: "not found for provider"},
		{name: "saved default with auth is used", settings: codingagent.Settings{DefaultProvider: "anthropic", DefaultModel: "claude-sonnet-4-5"}, want: "anthropic/claude-sonnet-4-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolateProviderAuthEnv(t)
			t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
			selected, err := selectStartupModel(tc.options, tc.settings, testServices(t, dir))
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("err = %v, want %q", err, tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if selected.Model == nil {
				t.Fatalf("no model selected, want %s", tc.want)
			}
			if got := selected.Model.ProviderMeta.ProviderID + "/" + selected.Model.ID; got != tc.want || selected.Thinking != tc.thinking {
				t.Fatalf("model = %s:%s, want %s:%s", got, selected.Thinking, tc.want, tc.thinking)
			}
			if tc.api != "" && selected.Model.ProviderMeta.API != tc.api {
				t.Fatalf("api = %q, want %q", selected.Model.ProviderMeta.API, tc.api)
			}
			if tc.warnContains != "" && !slices.ContainsFunc(selected.Warnings, func(w string) bool { return strings.Contains(w, tc.warnContains) }) {
				t.Fatalf("warnings = %q, want %q", selected.Warnings, tc.warnContains)
			}
		})
	}
}

// TestSelectStartupModelHidesTestFauxWithoutOptIn keeps the test-only
// provider out of normal runs (CH-021): without WOPR_TEST_FAUX=1, --model
// test-faux/... resolves like any unknown model.
func TestSelectStartupModelHidesTestFauxWithoutOptIn(t *testing.T) {
	dir := isolateProviderAuthEnv(t)
	t.Setenv("WOPR_TEST_FAUX", "")
	_, err := selectStartupModel(startupModelOptions{CLIModel: "test-faux/faux-1"}, codingagent.Settings{}, testServices(t, dir))
	if err == nil || !strings.Contains(err.Error(), `Model "test-faux/faux-1" not found`) {
		t.Fatalf("err = %v, want upstream's not-found error", err)
	}
}

func testServices(t *testing.T, dir string) *coding.Services {
	t.Helper()
	services, err := coding.NewServices(coding.ServicesOptions{CWD: dir, AgentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return services
}
