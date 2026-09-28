//go:build integration

// session_dir_test.go: --session-dir must place sessions where the caller
// asked, in every non-interactive mode.
//
// Every mode must use the SessionManager built from the resolved session dir.
// wopr once dispatched print/json/rpc before applying it, silently writing to
// WOPR_HOME instead.

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// emptyHome returns an empty WOPR_HOME with an agent directory.
func emptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

// sessionFiles lists session JSONL files beneath root.
func sessionFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // a missing tree simply has no sessions
		}
		if strings.HasSuffix(path, ".jsonl") {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// runWithSessionDir runs one non-interactive turn with --session-dir and
// returns the chosen dir and the isolated WOPR_HOME it must not write into.
func runWithSessionDir(t *testing.T, modeArgs []string, prompt string) (sessionDir, woprHome string) {
	t.Helper()
	sessionDir = filepath.Join(t.TempDir(), "chosen-sessions")
	woprHome = emptyHome(t)

	args := append([]string{"--model", "test-faux/echo", "--session-dir", sessionDir}, modeArgs...)
	cmd := exec.Command(buildBinary(t), append(args, prompt)...)
	cmd.Env = append(os.Environ(), "WOPR_HOME="+woprHome, "WOPR_QUIET_STARTUP=1", "WOPR_TEST_FAUX=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	return sessionDir, woprHome
}

func TestSessionDirHonoredInPrintMode(t *testing.T) {
	dir, home := runWithSessionDir(t, nil, "What is 20+22?")

	if got := sessionFiles(t, dir); len(got) == 0 {
		t.Errorf("no session written under --session-dir %s", dir)
	}
	if stray := sessionFiles(t, filepath.Join(home, "agent", "sessions")); len(stray) > 0 {
		t.Errorf("--session-dir was ignored; session written to WOPR_HOME instead: %v", stray)
	}
}

// Writing to the directory is only half the contract: resume must look there
// too, otherwise a caller can persist a session it can never reload.
func TestSessionDirUsedForResumeLookup(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "chosen-sessions")
	woprHome := emptyHome(t)
	const id = "session-dir-resume"

	run := func(prompt string) string {
		t.Helper()
		cmd := exec.Command(buildBinary(t),
			"--mode", "json", "--model", "test-faux/echo",
			"--session-dir", sessionDir, "--session-id", id, prompt)
		cmd.Env = append(os.Environ(), "WOPR_HOME="+woprHome, "WOPR_QUIET_STARTUP=1", "WOPR_TEST_FAUX=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run failed: %v\n%s", err, out)
		}
		return string(out)
	}

	run("Remember this exact code: the letters A L P H A and the digits 7 7 4 9")
	if out := run("What was the code I told you to remember?"); !strings.Contains(out, "ALPHA-7749") {
		t.Fatalf("resume did not read from --session-dir; want ALPHA-7749 in:\n%s", out)
	}
}
