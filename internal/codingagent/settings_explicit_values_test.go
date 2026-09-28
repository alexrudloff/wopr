package codingagent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSettingsFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSettingsExplicitZeroKeepsItsValue checks that an explicit 0 is a value, not a missing
// setting, and a project 0 overrides a global value.
func TestSettingsExplicitZeroKeepsItsValue(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	writeSettingsFixture(t, filepath.Join(agentDir, "settings.json"), `{
		"retry": {"maxRetries": 5, "baseDelayMs": 0, "provider": {"timeoutMs": 0, "maxRetries": 0}},
		"compaction": {"reserveTokens": 0, "keepRecentTokens": 0},
		"branchSummary": {"reserveTokens": 0}
	}`)
	writeSettingsFixture(t, filepath.Join(cwd, CONFIG_DIR_NAME, "settings.json"), `{"retry": {"maxRetries": 0}}`)
	sm := NewSettingsManagerWithProjectTrust(cwd, agentDir, true)

	retry := sm.GetRetrySettings()
	if retry.MaxRetries != 0 || retry.BaseDelayMs != 0 {
		t.Fatalf("retry = %+v, want maxRetries 0 (project) and baseDelayMs 0", retry)
	}
	if got := sm.GetCompactionSettings(); got.ReserveTokens != 0 || got.KeepRecentTokens != 0 {
		t.Fatalf("compaction = %+v, want zero tokens", got)
	}
	if got := sm.GetBranchSummarySettings().ReserveTokens; got != 0 {
		t.Fatalf("branch summary reserve = %d, want 0", got)
	}
	if got := sm.GetProviderRetrySettings(); got.MaxRetries != 0 || got.TimeoutMs != 0 {
		t.Fatalf("provider retry = %+v", got)
	}
	if got := mustTimeout(t)(sm.GetProviderRequestTimeoutMs()); got != 0 {
		t.Fatalf("provider request timeout = %d, want the explicit 0", got)
	}

	if err := sm.UpdateGlobal(func(s *Settings) { s.Retry.Enabled = new(false) }); err != nil {
		t.Fatal(err)
	}
	reloaded := NewSettingsManagerWithProjectTrust(t.TempDir(), agentDir, false)
	if got := reloaded.GetRetrySettings(); got.MaxRetries != 5 || got.BaseDelayMs != 0 || got.Enabled {
		t.Fatalf("reloaded global retry = %+v, want maxRetries 5, baseDelayMs 0, disabled", got)
	}
	if got := reloaded.GetCompactionSettings(); got.ReserveTokens != 0 {
		t.Fatalf("reloaded compaction = %+v, want the explicit 0 kept on save", got)
	}
}

// TestSettingsDropsOnlyTheBadKey covers CH-019: a mistyped element inside an
// array (here a numeric skill path) drops that setting, not the whole settings
// file.
func TestSettingsDropsOnlyTheBadKey(t *testing.T) {
	agentDir := t.TempDir()
	writeSettingsFixture(t, filepath.Join(agentDir, "settings.json"), `{"theme":"dark","skills":["a",5,"b"]}`)
	sm := NewSettingsManagerWithProjectTrust(t.TempDir(), agentDir, false)
	if errs := sm.DrainErrors(); len(errs) != 0 {
		t.Fatalf("load errors = %+v", errs)
	}
	if got := sm.Get().Theme; got != "dark" {
		t.Fatalf("theme = %q, want dark", got)
	}
	if skills := sm.Get().Skills; len(skills) != 0 {
		t.Fatalf("skills = %+v, want the mistyped setting dropped", skills)
	}
}
