package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	woprBinaryOnce sync.Once
	woprBinaryPath string
	woprBinaryErr  error
	// woprBinaryDir holds the shared binary; TestMain sets and removes it.
	woprBinaryDir string
	// buildEnv is the environment before TestMain isolates it, so the build
	// uses the developer's Go cache instead of recompiling from scratch.
	buildEnv []string
)

// woprBinary builds the real wopr binary once per test process and returns
// its path.
func woprBinary(t *testing.T) string {
	t.Helper()
	woprBinaryOnce.Do(func() {
		dir := woprBinaryDir
		if dir == "" {
			dir = os.TempDir()
		}
		out := filepath.Join(dir, "wopr-test")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Env = buildEnv
		if data, err := cmd.CombinedOutput(); err != nil {
			woprBinaryErr = fmt.Errorf("build wopr: %w\n%s", err, data)
			return
		}
		woprBinaryPath = out
	})
	if woprBinaryErr != nil {
		t.Fatal(woprBinaryErr)
	}
	return woprBinaryPath
}
