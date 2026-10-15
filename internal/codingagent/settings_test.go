package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGetServerAllowedRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := Settings{Server: &ServerSettings{AllowedRoots: []string{"~/root", "file:///srv/data"}}}
	got, err := s.GetServerAllowedRoots()
	if err != nil {
		t.Fatalf("GetServerAllowedRoots: %v", err)
	}
	want := []string{filepath.Join(home, "root"), "/srv/data"}
	if !slices.Equal(got, want) {
		t.Fatalf("roots = %q, want %q", got, want)
	}

	// No server settings, or an empty list, is unrestricted.
	if got, err := (Settings{}).GetServerAllowedRoots(); err != nil || got != nil {
		t.Fatalf("empty = %q, %v; want nil, nil", got, err)
	}

	// An invalid file URL is an error.
	s = Settings{Server: &ServerSettings{AllowedRoots: []string{"file://\x7f"}}}
	if _, err := s.GetServerAllowedRoots(); err == nil {
		t.Fatal("invalid file URL succeeded, want error")
	}
}

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

// TestUnmarshalJSONRejectsUndecodableServerGuardrail covers a server value that
// decodeTolerant would silently drop, which would leave the serve allowlist
// guardrail off. Every other setting stays tolerant.
func TestUnmarshalJSONRejectsUndecodableServerGuardrail(t *testing.T) {
	reject := []string{
		`{"server":5}`,
		`{"server":{"allowedRoots":"x"}}`,
		`{"server":{"allowedRoots":[5]}}`,
		`{"server":{"allowedRoots":{}}}`,
		`{"server":{"allowedRoots":["/a",5]}}`,
		// encoding/json matches struct keys case-insensitively, so these must
		// be classified the same as their lowercase forms.
		`{"Server":{"allowedRoots":"x"}}`,
		`{"SERVER":{"allowedRoots":[5]}}`,
		`{"server":{"AllowedRoots":"x"}}`,
		`{"Server":{"AllowedRoots":{}}}`,
	}
	for _, body := range reject {
		var s Settings
		err := json.Unmarshal([]byte(body), &s)
		if err == nil || !strings.Contains(err.Error(), "server") {
			t.Fatalf("Unmarshal(%s) error = %v, want it to name server", body, err)
		}
	}

	// A wrong-typed port next to a valid list drops only port; the guardrail
	// survives. This guards the decodeTolerant interaction.
	var s Settings
	if err := json.Unmarshal([]byte(`{"server":{"port":"x","allowedRoots":["/a"]}}`), &s); err != nil {
		t.Fatalf("Unmarshal dropped only the bad port: %v", err)
	}
	if s.Server == nil || !slices.Equal(s.Server.AllowedRoots, []string{"/a"}) {
		t.Fatalf("server = %+v, want allowedRoots [/a]", s.Server)
	}

	// encoding/json resolves the case variants, so the guard must not
	// over-reject a valid case-variant list.
	var caseVariant Settings
	if err := json.Unmarshal([]byte(`{"Server":{"AllowedRoots":["/a"]}}`), &caseVariant); err != nil {
		t.Fatalf("Unmarshal case-variant list = %v, want nil", err)
	}
	if caseVariant.Server == nil || !slices.Equal(caseVariant.Server.AllowedRoots, []string{"/a"}) {
		t.Fatalf("server = %+v, want allowedRoots [/a]", caseVariant.Server)
	}

	if err := json.Unmarshal([]byte(`{"server":null}`), &s); err != nil {
		t.Fatalf("Unmarshal(server:null) = %v, want nil", err)
	}
	if s.Server != nil {
		t.Fatalf("server = %+v, want nil", s.Server)
	}
}

// TestUnmarshalJSONRejectsNearMissAllowedRootsKey covers the fail-closed
// guardrail: a settings.server key that looks like a misspelled allowedRoots
// and no valid allowedRoots present is an error, while an unrelated unknown key
// and a near-miss beside a valid allowedRoots stay tolerated.
func TestUnmarshalJSONRejectsNearMissAllowedRootsKey(t *testing.T) {
	reject := []string{
		`{"server":{"allowedRoot":"/srv"}}`,
		`{"server":{"allowedRoots ":["/srv"]}}`,
		`{"server":{" allowedRoots":["/srv"]}}`,
		// A leading zero-width space (U+200B) is padding TrimSpace would miss.
		"{\"server\":{\"\u200ballowedRoots\":[\"/srv\"]}}",
	}
	for _, body := range reject {
		var s Settings
		err := json.Unmarshal([]byte(body), &s)
		if err == nil || !strings.Contains(err.Error(), "did you mean server.allowedRoots") {
			t.Fatalf("Unmarshal(%s) error = %v, want a near-miss allowedRoots error", body, err)
		}
	}

	// An unrelated unknown server key is tolerated, not a near-miss.
	var tolerant Settings
	if err := json.Unmarshal([]byte(`{"server":{"allowedHosts":["x"]}}`), &tolerant); err != nil {
		t.Fatalf("Unmarshal(allowedHosts) = %v, want nil", err)
	}
	if tolerant.Server == nil || len(tolerant.Server.AllowedRoots) != 0 {
		t.Fatalf("server = %+v, want empty allowedRoots", tolerant.Server)
	}

	// A valid allowedRoots wins over a near-miss key beside it.
	var validWins Settings
	if err := json.Unmarshal([]byte(`{"server":{"allowedRoots":["/a"],"allowedRoot":["/b"]}}`), &validWins); err != nil {
		t.Fatalf("Unmarshal(valid beside near-miss) = %v, want nil", err)
	}
	if validWins.Server == nil || !slices.Equal(validWins.Server.AllowedRoots, []string{"/a"}) {
		t.Fatalf("server = %+v, want allowedRoots [/a]", validWins.Server)
	}

	// An empty array is present-and-valid (unrestricted), so it wins too.
	var emptyValid Settings
	if err := json.Unmarshal([]byte(`{"server":{"allowedRoots":[],"allowedRoot":["/b"]}}`), &emptyValid); err != nil {
		t.Fatalf("Unmarshal(empty allowedRoots beside near-miss) = %v, want nil", err)
	}
}

// TestUnmarshalJSONRejectsNearMissServerKey covers the outer-key fail-closed
// guardrail: a top-level key that looks like a misspelled server and no valid
// server.allowedRoots present is an error, while unrelated top-level keys and a
// near-miss beside a valid server stay tolerated. The padded object's inner keys
// are never inspected, so these fixtures exercise the outer path only.
func TestUnmarshalJSONRejectsNearMissServerKey(t *testing.T) {
	reject := []string{
		`{"server ":{"allowedRoots":["/srv"]}}`,
		// A leading zero-width space (U+200B) is padding TrimSpace would miss.
		"{\"\u200bserver\":{\"allowedRoots\":[\"/srv\"]}}",
		`{"servers":{"allowedRoots":["/srv"]}}`,
		// An extra ASCII letter before the stem: prefix-only would miss it, the
		// containment rule catches it.
		`{"aserver":{"allowedRoots":["/srv"]}}`,
		// Interior padding defeats a leading/trailing-only trim: a zero-width
		// space (U+200B, Cf) and a plain space between the stem and the tail.
		"{\"serv\u200ber\":{\"allowedRoots\":[\"/srv\"]}}",
		`{"ser ver":{"allowedRoots":["/srv"]}}`,
		// Invisible runes outside a drop-list category still pad the stem:
		// BRAILLE PATTERN BLANK (U+2800, So) leading, HANGUL CHOSEONG FILLER
		// (U+115F, Lo) interior. The keep-only-a-z rule is category-independent.
		"{\"\\u2800server\":{\"allowedRoots\":[\"/srv\"]}}",
		"{\"serv\\u115fer\":{\"allowedRoots\":[\"/srv\"]}}",
		// A port-only near-miss still refuses rather than dropping port silently.
		`{"server ":{"port":8080}}`,
		// Non-ASCII runes are dropped, not case-folded: KELVIN SIGN (U+212A) and
		// I WITH DOT ABOVE (U+0130) must not survive as k/i inside the stem.
		`{"serv\u212Aer":{"allowedRoots":["/srv"]}}`,
		`{"serv\u0130er":{"allowedRoots":["/srv"]}}`,
	}
	for _, body := range reject {
		var s Settings
		err := json.Unmarshal([]byte(body), &s)
		if err == nil || !strings.Contains(err.Error(), "did you mean server?") {
			t.Fatalf("Unmarshal(%s) error = %v, want an outer near-miss server error", body, err)
		}
	}

	// Unrelated top-level keys stay tolerant.
	var tolerant Settings
	if err := json.Unmarshal([]byte(`{"theme":"dark"}`), &tolerant); err != nil {
		t.Fatalf("Unmarshal(theme) = %v, want nil", err)
	}
	var unrelated Settings
	if err := json.Unmarshal([]byte(`{"defaultProvider":"x","server":{"allowedRoots":["/a"]}}`), &unrelated); err != nil {
		t.Fatalf("Unmarshal(defaultProvider beside server) = %v, want nil", err)
	}
	if unrelated.Server == nil || !slices.Equal(unrelated.Server.AllowedRoots, []string{"/a"}) {
		t.Fatalf("server = %+v, want allowedRoots [/a]", unrelated.Server)
	}

	// A valid exact server wins over a near-miss beside it, and the near-miss
	// key reaches the unknown-key warning path.
	var validWins Settings
	if err := json.Unmarshal([]byte(`{"server":{"allowedRoots":["/a"]},"server ":{"allowedRoots":["/b"]}}`), &validWins); err != nil {
		t.Fatalf("Unmarshal(valid beside near-miss) = %v, want nil", err)
	}
	if validWins.Server == nil || !slices.Equal(validWins.Server.AllowedRoots, []string{"/a"}) {
		t.Fatalf("server = %+v, want allowedRoots [/a]", validWins.Server)
	}
	if !slices.Contains(validWins.serverUnknownKeys, "server ") {
		t.Fatalf("serverUnknownKeys = %v, want it to contain \"server \"", validWins.serverUnknownKeys)
	}
}

// TestLoadWarnsOnUnknownServerKey checks an unrelated unknown settings.server
// key and a near-miss outer server key beside a valid one neither fail the load
// nor hide: both reach the warning accessor.
func TestLoadWarnsOnUnknownServerKey(t *testing.T) {
	agentDir := t.TempDir()
	body := `{"server":{"allowedRoots":["/a"],"allowedHosts":["x"]},"server ":{"allowedRoots":["/b"]}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sm := NewSettingsManagerWithProjectTrust(t.TempDir(), agentDir, false)
	if err := sm.GlobalLoadError(); err != nil {
		t.Fatalf("GlobalLoadError() = %v, want nil", err)
	}
	keys := sm.GlobalUnknownServerKeys()
	if !slices.Contains(keys, "allowedHosts") {
		t.Fatalf("GlobalUnknownServerKeys() = %v, want it to contain allowedHosts", keys)
	}
	if !slices.Contains(keys, "server ") {
		t.Fatalf("GlobalUnknownServerKeys() = %v, want it to contain \"server \"", keys)
	}
}
