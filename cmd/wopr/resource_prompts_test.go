package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePromptInputsUsesTrustedProjectBeforeGlobal(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	projectDir := filepath.Join(cwd, ".wopr")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(agentDir, "SYSTEM.md"):          "global system",
		filepath.Join(agentDir, "APPEND_SYSTEM.md"):   "global append",
		filepath.Join(projectDir, "SYSTEM.md"):        "project system",
		filepath.Join(projectDir, "APPEND_SYSTEM.md"): "project append",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	trusted := resolvePromptInputs(cwd, agentDir, CLIFlags{}, true)
	if trusted.custom != "project system" || trusted.append != "project append" {
		t.Fatalf("trusted = %#v", trusted)
	}
	untrusted := resolvePromptInputs(cwd, agentDir, CLIFlags{}, false)
	if untrusted.custom != "global system" || untrusted.append != "global append" {
		t.Fatalf("untrusted = %#v", untrusted)
	}
}
