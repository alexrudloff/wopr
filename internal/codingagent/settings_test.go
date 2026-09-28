package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsManagerUntrustedProjectIgnoresAndCannotWriteProjectSettings(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, CONFIG_DIR_NAME), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"defaultModel":"global"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, CONFIG_DIR_NAME, "settings.json"), []byte(`{"defaultModel":"project","defaultProjectTrust":"always"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sm := NewSettingsManagerWithProjectTrust(cwd, agentDir, false)
	if sm.IsProjectTrusted() {
		t.Fatal("untrusted settings manager reports trusted")
	}
	if got := sm.Get().DefaultModel; got != "global" {
		t.Fatalf("merged default model = %q, want global", got)
	}
	if got := sm.GetProjectSettings(); got.DefaultModel != "" || got.DefaultProjectTrust != "" {
		t.Fatalf("project settings leaked while untrusted: %+v", got)
	}
	if err := sm.UpdateProject(func(s *Settings) { s.DefaultModel = "written" }); err == nil {
		t.Fatal("UpdateProject succeeded while untrusted")
	}

	sm.SetProjectTrusted(true)
	if !sm.IsProjectTrusted() || sm.Get().DefaultModel != "project" {
		t.Fatalf("trusted reload did not expose project settings: %+v", sm.Get())
	}
}

func writeModelsJSON(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestModelRegistryResolvesBangCmd proves the !cmd path is honored
// at the registry call site.
func TestModelRegistryResolvesBangCmd(t *testing.T) {
	dir := t.TempDir()
	writeModelsJSON(t, dir, `{
		"providers": {
			"anthropic": {
				"apiKey": "!echo sk-from-shell",
				"models": [{"id": "claude-x"}]
			}
		}
	}`)

	r := NewModelRegistry(dir)
	got, ok := r.Resolve("anthropic", "claude-x")
	if !ok {
		t.Fatalf("Resolve missed anthropic/claude-x")
	}
	if got.APIKey != "sk-from-shell" {
		t.Errorf("!cmd not executed; got %q", got.APIKey)
	}
}

// ─── Compaction settings ──────────────────────────────────────────────────────

// retry.provider.maxRetryDelayMs distinguishes unset (nil -> 60s default cap)
// from an explicit 0, which disables the cap. A plain int collapsed explicit
// 0 into the default, hiding the
// disable-cap intent from the provider retry transport.
func TestSettingsManager_GetProviderRetrySettings_MaxRetryDelayMs(t *testing.T) {
	zero := 0
	five := 5000
	cases := []struct {
		name  string
		field *int
		want  int
	}{
		{"unset defaults to 60s cap", nil, 60000},
		{"explicit zero disables cap", &zero, 0},
		{"explicit value passes through", &five, 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := &SettingsManager{merged: Settings{
				Retry: &RetrySettingsJSON{Provider: &ProviderRetrySettings{MaxRetryDelayMs: tc.field}},
			}}
			if got := sm.GetProviderRetrySettings().MaxRetryDelayMs; got != tc.want {
				t.Fatalf("MaxRetryDelayMs = %d, want %d", got, tc.want)
			}
		})
	}
}

// mustTimeout unwraps a timeout getter's result, failing the test on error.
func mustTimeout(t *testing.T) func(int, error) int {
	return func(timeoutMs int, err error) int {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return timeoutMs
	}
}

func TestSettingsManager_UpdateGlobalRefusesToClobberInvalidFile(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	path := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"defaultModel":`), 0o644); err != nil {
		t.Fatal(err)
	}

	sm := NewSettingsManager(cwd, agentDir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.SetTheme("dark"); err == nil {
		t.Fatal("SetTheme unexpectedly succeeded with invalid global settings file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid settings file was clobbered")
	}
}

func TestSettingsManager_UpdateGlobalPreservesExternalUnrelatedChanges(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	path := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","packages":["npm:old"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sm := NewSettingsManager(cwd, agentDir)
	if err := os.WriteFile(path, []byte(`{"theme":"dark","packages":[],"externalField":"preserve"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sm.SetTheme("light"); err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if packages, ok := persisted["packages"].([]any); !ok || len(packages) != 0 {
		t.Fatalf("external packages change was clobbered: %#v", persisted)
	}
	if persisted["externalField"] != "preserve" || persisted["theme"] != "light" {
		t.Fatalf("persisted settings = %#v", persisted)
	}
}

func TestSettingsManager_ConcurrentManagersPreserveDistinctWrites(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	first := NewSettingsManager(cwd, agentDir)
	second := NewSettingsManager(cwd, agentDir)
	start := make(chan struct{})
	errors := make(chan error, 2)
	go func() {
		<-start
		errors <- first.SetTheme("light")
	}()
	go func() {
		<-start
		errors <- second.UpdateGlobal(func(s *Settings) { s.DefaultModel = "model" })
	}()
	close(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	reloaded := NewSettingsManager(cwd, agentDir)
	if got := reloaded.Get(); got.Theme != "light" || got.DefaultModel != "model" {
		t.Fatalf("concurrent writes lost: %+v", reloaded.GetGlobalSettings())
	}
}

// ─── models.json schema tests ────────────────────────────────────────────────

func TestMergeSettings_DeepMergeAndFalseOverride(t *testing.T) {
	global := Settings{
		HideThinkingBlock: new(true),
		QuietStartup:      new(true),
		CollapseChangelog: new(true),
		Terminal:          &TerminalSettings{ShowImages: new(true), ImageWidthCells: 60},
		Images:            &ImageSettings{AutoResize: new(true), BlockImages: new(true)},
		BranchSummary:     &BranchSummarySettings{ReserveTokens: new(1000), SkipPrompt: new(true)},
		ThinkingBudgets:   &ThinkingBudgetsSettings{Low: new(100)},
		Markdown:          &MarkdownSettings{Mermaid: "off"},
	}
	project := Settings{
		HideThinkingBlock: new(false),
		QuietStartup:      new(false),
		CollapseChangelog: new(false),
		Terminal:          &TerminalSettings{ShowImages: new(false)},
		Images:            &ImageSettings{AutoResize: new(false), BlockImages: new(false)},
		BranchSummary:     &BranchSummarySettings{SkipPrompt: new(false)},
		ThinkingBudgets:   &ThinkingBudgetsSettings{High: new(400)},
		Markdown:          &MarkdownSettings{Mermaid: "final"},
	}
	merged := mergeSettings(global, project)
	if merged.GetHideThinkingBlock() || merged.GetQuietStartup() || merged.GetCollapseChangelog() {
		t.Fatalf("false overrides failed: %+v", merged)
	}
	if merged.GetShowImages() || merged.GetImageWidthCells() != 60 || merged.GetImageAutoResize() || merged.GetBlockImages() {
		t.Fatalf("terminal/image overrides failed: %+v %+v", merged.Terminal, merged.Images)
	}
	if b := merged.GetBranchSummarySettings(); b.ReserveTokens != 1000 || b.SkipPrompt {
		t.Fatalf("branch summary deep merge failed: %+v", b)
	}
	if merged.ThinkingBudgets == nil || merged.ThinkingBudgets.Low == nil || *merged.ThinkingBudgets.Low != 100 || merged.ThinkingBudgets.High == nil || *merged.ThinkingBudgets.High != 400 {
		t.Fatalf("thinking budgets deep merge failed: %+v", merged.ThinkingBudgets)
	}
	if merged.GetMermaidRenderingMode() != "final" {
		t.Fatalf("markdown merge failed: %+v", merged.Markdown)
	}
}

// TestSettingsDefaults checks the defaults that affect cost, context and
// safety resolve from an empty SettingsManager, and that a project file
// cannot raise project trust.
func TestSettingsDefaults(t *testing.T) {
	sm := &SettingsManager{}
	c := sm.GetCompactionSettings()
	// Zero token settings scale with the model's window.
	if !c.Enabled || c.ReserveTokens != 0 || c.KeepRecentTokens != 0 {
		t.Fatalf("compaction defaults = %+v", c)
	}
	if got, err := sm.GetHttpIdleTimeoutMs(); err != nil || got != defaultHTTPIdleTimeoutMs {
		t.Fatalf("GetHttpIdleTimeoutMs() = %d, %v", got, err)
	}
	if DefaultThinkingLevel != "medium" {
		t.Fatalf("DefaultThinkingLevel = %q, want medium", DefaultThinkingLevel)
	}
	if got := sm.GetDefaultProjectTrust(); got != "ask" {
		t.Fatalf("GetDefaultProjectTrust() = %q, want ask", got)
	}
	sm = &SettingsManager{global: Settings{DefaultProjectTrust: "always"}, merged: Settings{DefaultProjectTrust: "never"}}
	if got := sm.GetDefaultProjectTrust(); got != "always" {
		t.Fatalf("project trust override leaked: got %q, want global always", got)
	}
}
