package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// `wopr config --local` writes project settings, so it must refuse an
// untrusted project.
func TestRunConfigCommandLocalRequiresTrustedProject(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("WOPR_HOME", home)
	t.Setenv("WOPR_CODING_AGENT_DIR", filepath.Join(home, "agent"))
	t.Chdir(cwd)
	_, stderr, code := captureStdoutStderr(t, func() int {
		return runConfigCommand([]string{"--local", "--no-approve"})
	})
	if code != 1 || !strings.Contains(stderr, "project is not trusted") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}
