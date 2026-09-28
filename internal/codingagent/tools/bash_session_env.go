package tools

import (
	"context"
	"os"
	"slices"
	"strings"

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
	env := os.Environ()
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
