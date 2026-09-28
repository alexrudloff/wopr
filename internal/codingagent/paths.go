package codingagent

import (
	"os"
	"path/filepath"
	"strings"
)

// AppName is the binary/CLI name.
const AppName = "wopr"

// CONFIG_DIR_NAME is the per-project config directory name.
const CONFIG_DIR_NAME = "." + AppName

// ENV_AGENT_DIR overrides the config directory.
const ENV_AGENT_DIR = "WOPR_CODING_AGENT_DIR"

// ENV_SESSION_DIR overrides the session storage directory.
const ENV_SESSION_DIR = "WOPR_CODING_AGENT_SESSION_DIR"

// AgentDir returns the writable agent directory: $WOPR_CODING_AGENT_DIR
// (with ~ expanded), else DefaultAgentDir.
func AgentDir() string {
	if configured := os.Getenv(ENV_AGENT_DIR); configured != "" {
		return ExpandTildePath(configured)
	}
	return DefaultAgentDir()
}

// ProjectConfigDir returns the workspace-local wopr configuration root.
func ProjectConfigDir(cwd string) string {
	return filepath.Join(cwd, CONFIG_DIR_NAME)
}

// ─── Config Root ──────────────────────────────────────────────────────────────

// ConfigRoot returns the wopr configuration root directory.
//
// Resolution order:
//  1. $WOPR_HOME if set and non-empty
//  2. $XDG_CONFIG_HOME/wopr if XDG_CONFIG_HOME is set
//  3. ~/.wopr (default)
//
// All wopr state: agent dir, auth.json, sessions, agents, subagent output -
// lives under this root.
func ConfigRoot() string {
	if v := os.Getenv("WOPR_HOME"); v != "" {
		return ExpandTildePath(v)
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(ExpandTildePath(v), "wopr")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wopr")
}

// CanonicalizePath resolves a path to its canonical filesystem form,
// following symlinks. If resolution fails (for example because the path
// does not exist yet), it falls back to the raw path.
func CanonicalizePath(path string) string {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return canonical
}

// ExpandTildePath expands a leading ~ in a filesystem path.
func ExpandTildePath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, rest)
	}
	return path
}

func resolveAgainstCwd(filePath, cwd string) string {
	if filepath.IsAbs(filePath) {
		return filepath.Clean(filePath)
	}
	return filepath.Clean(filepath.Join(cwd, filePath))
}
