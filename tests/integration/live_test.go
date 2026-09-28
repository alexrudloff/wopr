//go:build integration && live

// Live tier: two print-mode runs against a real model. They are manual (not
// in `make check`); run them with `automation/ci/integration-tests.sh live`.
//
// The model comes from WOPR_LIVE_PROVIDER and WOPR_LIVE_MODEL; the tests
// skip without them. Each test copies auth.json and
// models.json from $WOPR_HOME/agent (or ~/.wopr/agent) into an isolated
// WOPR_HOME, and skips when neither exists.

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// liveModel is the model to run against, from WOPR_LIVE_PROVIDER and
// WOPR_LIVE_MODEL; it skips the test when either is unset.
func liveModel(t *testing.T) string {
	t.Helper()
	provider, model := os.Getenv("WOPR_LIVE_PROVIDER"), os.Getenv("WOPR_LIVE_MODEL")
	if provider == "" || model == "" {
		t.Skip("set WOPR_LIVE_PROVIDER and WOPR_LIVE_MODEL to run the live tier")
	}
	return provider + "/" + model
}

// isolatedHome returns a temporary WOPR_HOME holding copies of the user's
// provider configuration, so sessions never land in the real ~/.wopr.
func isolatedHome(t *testing.T) string {
	t.Helper()
	source := os.Getenv("WOPR_HOME")
	if source == "" {
		home, _ := os.UserHomeDir()
		source = filepath.Join(home, ".wopr")
	}
	tmp := t.TempDir()
	agentDir := filepath.Join(tmp, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	copied := 0
	for _, name := range []string{"auth.json", "models.json"} {
		data, err := os.ReadFile(filepath.Join(source, "agent", name))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(agentDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		copied++
	}
	if copied == 0 {
		t.Skipf("no auth.json or models.json under %s/agent: configure %s to run live tests", source, liveModel(t))
	}
	return tmp
}

func runLivePrint(t *testing.T, cwd, prompt string) string {
	t.Helper()
	cmd := exec.Command(buildBinary(t), "--model", liveModel(t), "--no-session", "--print", prompt)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "WOPR_HOME="+isolatedHome(t), "WOPR_QUIET_STARTUP=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wopr --print (%s) failed: %v\n%s", liveModel(t), err, out)
	}
	return string(out)
}

func TestLive_PrintModeBasicReply(t *testing.T) {
	out := runLivePrint(t, t.TempDir(), "Reply with EXACTLY the single word PONG and nothing else.")
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Fatalf("expected PONG in reply, got:\n%s", out)
	}
}

// The sentinel only reaches the reply if the model ran a tool to read it.
func TestLive_PrintModeUsesBashTool(t *testing.T) {
	cwd := t.TempDir()
	const sentinel = "ZEPHYR-7142-MAGENTA"
	if err := os.WriteFile(filepath.Join(cwd, "secret.txt"), []byte(sentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runLivePrint(t, cwd, "Use the bash tool to cat secret.txt in the current directory and report its contents.")
	if !strings.Contains(out, sentinel) {
		t.Fatalf("expected sentinel %q in output; got:\n%s", sentinel, out)
	}
}
