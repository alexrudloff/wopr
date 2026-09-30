package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/agent"
)

// sessionVariables are the variables the bash tool controls.
var sessionVariables = []string{"WOPR_SESSION_ID", "WOPR_SESSION_FILE", "WOPR_PROVIDER", "WOPR_MODEL", "WOPR_REASONING_LEVEL"}

// sessionGuideline is the bash prompt guideline for the variables.
const sessionGuideline = "You can inspect WOPR_* environment variables for current model and session details."

// GetShellEnv returns the process environment with
// binDir (<agentDir>/bin, where managed rg and fd live)
// prepended to PATH unless PATH already lists it. The PATH key is matched
// case-insensitively, as on Windows. An empty binDir leaves PATH unchanged.
func GetShellEnv(binDir string) []string {
	env := withPythonShims(os.Environ())
	if binDir == "" {
		return env
	}
	pathIndex := slices.IndexFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return strings.EqualFold(name, "path")
	})
	if pathIndex < 0 {
		return append(env, "PATH="+binDir)
	}
	name, current, _ := strings.Cut(env[pathIndex], "=")
	if slices.Contains(strings.Split(current, string(os.PathListSeparator)), binDir) {
		return env
	}
	updated := binDir
	if current != "" {
		updated += string(os.PathListSeparator) + current
	}
	env[pathIndex] = name + "=" + updated
	return env
}

// sessionEnvironment always removes
// inherited session variables, so a nested wopr never sees its parent's values,
// then adds the current session's values when expose is set.
func sessionEnvironment(ctx context.Context, expose bool, binDir string) []string {
	env := slices.DeleteFunc(GetShellEnv(binDir), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(sessionVariables, name)
	})
	session, ok := agent.ToolEnvironmentFrom(ctx)
	if ok && session.TempDir != "" && os.MkdirAll(session.TempDir, 0o700) == nil {
		// The session's own temp directory, so files a command creates
		// through mktemp or a language's temp API can be cleaned up with it.
		env = slices.DeleteFunc(env, func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")
			return name == "TMPDIR" || strings.EqualFold(name, "TMP") || strings.EqualFold(name, "TEMP")
		})
		env = append(env, "TMPDIR="+session.TempDir)
		if runtime.GOOS == "windows" {
			env = append(env, "TMP="+session.TempDir, "TEMP="+session.TempDir)
		}
	}
	if !expose || !ok {
		return env
	}
	for _, v := range [][2]string{
		{"WOPR_SESSION_ID", session.SessionID},
		{"WOPR_SESSION_FILE", session.SessionFile},
		{"WOPR_PROVIDER", session.Provider},
		{"WOPR_MODEL", session.Model},
		{"WOPR_REASONING_LEVEL", session.ThinkingLevel},
	} {
		if v[1] != "" {
			env = append(env, v[0]+"="+v[1])
		}
	}
	return env
}

// pythonShims maps a command models reach for to the one to run when only
// the latter is installed, as on macOS: python → python3, pip → pip3.
var pythonShims = [][2]string{{"python", "python3"}, {"pip", "pip3"}}

var (
	shimOnce sync.Once
	shimDir  string
)

// withPythonShims appends to PATH a directory linking python and pip to
// python3 and pip3 when only those exist. Appended last, it never shadows a
// real python.
func withPythonShims(env []string) []string {
	shimOnce.Do(func() { shimDir = makePythonShims() })
	if shimDir == "" {
		return env
	}
	for i, kv := range env {
		if name, current, _ := strings.Cut(kv, "="); strings.EqualFold(name, "path") {
			env[i] = name + "=" + current + string(os.PathListSeparator) + shimDir
			return env
		}
	}
	return append(env, "PATH="+shimDir)
}

// makePythonShims creates the shim directory under the user cache and
// returns it, or "" when no shim is needed or it can't be made.
func makePythonShims() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(cache, "wopr", "shims")
	made := false
	for _, shim := range pythonShims {
		if _, err := exec.LookPath(shim[0]); err == nil {
			continue
		}
		target, err := exec.LookPath(shim[1])
		if err != nil {
			continue
		}
		if os.MkdirAll(dir, 0o755) != nil {
			return ""
		}
		link := filepath.Join(dir, shim[0])
		if current, err := os.Readlink(link); err != nil || current != target {
			_ = os.Remove(link)
			if os.Symlink(target, link) != nil {
				continue
			}
		}
		made = true
	}
	if !made {
		return ""
	}
	return dir
}
