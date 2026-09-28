package codingagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/ai/aitest"

	"github.com/alexrudloff/wopr/ai"
)

// execEchoHelperEnv, when set to "1", makes this test binary behave like a
// portable `echo`: print the remaining arguments space-joined with a
// trailing newline, then exit, without running any tests. A test that needs
// to exec a real, natively executable command (through exec, which spawns
// the requested command directly with no shell) sets this in the child's
// environment and passes os.Executable() (this same test binary) as the
// command, so the same test works on every OS the test binary runs on,
// including native Windows, instead of depending on /bin/echo or a
// cmd.exe-only builtin. Mirrors cmd/wopr/testmain_test.go's identical helper.
const execEchoHelperEnv = "WOPR_TEST_EXEC_ECHO_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(execEchoHelperEnv) == "1" {
		fmt.Println(strings.Join(os.Args[1:], " "))
		os.Exit(0)
	}
	// Tests that leave AgentDir empty fall back to DefaultAgentDir; point it
	// at a temporary root so no test writes the developer's real config, and
	// keep real credentials from selecting a provider.
	testRoot, err := os.MkdirTemp("", "wopr-codingagent-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test config root:", err)
		os.Exit(2)
	}
	for _, key := range append(ai.ProviderCredentialEnvVars(), "WOPR_CODING_AGENT_DIR", "WOPR_CODING_AGENT_SESSION_DIR") {
		_ = os.Unsetenv(key)
	}
	for key, value := range aitest.HermeticCredentialEnv(testRoot) {
		_ = os.Setenv(key, value)
	}
	_ = os.Setenv("WOPR_HOME", filepath.Join(testRoot, "wopr"))
	code := m.Run()
	_ = os.RemoveAll(testRoot)
	os.Exit(code)
}
