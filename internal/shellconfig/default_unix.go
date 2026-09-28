//go:build !windows

package shellconfig

import (
	"os"
	"os/exec"
)

// Default resolves the shell on unix. It never errors.
func Default() (Config, error) {
	return unixDefault(fileExists, exec.LookPath), nil
}

// unixDefault tries /bin/bash, then bash on PATH, then falls back to sh
// (resolved through PATH at spawn time).
func unixDefault(exists func(string) bool, lookPath func(string) (string, error)) Config {
	if exists("/bin/bash") {
		return ForBash("/bin/bash")
	}
	if bash, err := lookPath("bash"); err == nil && bash != "" {
		return ForBash(bash)
	}
	return Config{Path: "sh", Args: []string{"-c"}}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
