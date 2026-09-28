package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrudloff/wopr/ai/aitest"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
)

// TestMain isolates the config root and provider credentials.
func TestMain(m *testing.M) {
	buildEnv = os.Environ()
	testRoot, err := os.MkdirTemp("", "wopr-command-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test home:", err)
		os.Exit(2)
	}
	// A developer's real provider credentials must not select a provider in
	// tests.
	for _, key := range append(append(ai.ProviderCredentialEnvVars(), web.SearchEnvVars()...), "WOPR_CODING_AGENT_DIR", "WOPR_CODING_AGENT_SESSION_DIR") {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "clear inherited %s: %v\n", key, err)
			_ = os.RemoveAll(testRoot)
			os.Exit(2)
		}
	}
	for key, value := range aitest.HermeticCredentialEnv(testRoot) {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "isolate %s: %v\n", key, err)
			_ = os.RemoveAll(testRoot)
			os.Exit(2)
		}
	}
	for key, value := range map[string]string{
		"WOPR_HOME":       filepath.Join(testRoot, "wopr"),
		"XDG_CONFIG_HOME": filepath.Join(testRoot, "config"),
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "set isolated %s: %v\n", key, err)
			_ = os.RemoveAll(testRoot)
			os.Exit(2)
		}
	}
	woprBinaryDir = testRoot
	code := m.Run()
	if err := os.RemoveAll(testRoot); err != nil {
		fmt.Fprintln(os.Stderr, "remove isolated test home:", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}
