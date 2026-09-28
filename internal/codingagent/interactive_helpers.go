package codingagent

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/tui"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (m *InteractiveMode) formatLoadedResourceRef(path, display string) string {
	display = cmp.Or(display, filepath.Base(path))
	info, ok := m.resourceSourceInfo[path]
	if !ok {
		return fmt.Sprintf("%s: %s", display, shortenPath(path))
	}
	label := info.Source
	if info.Origin == "top-level" || label == "" {
		label = "local"
	}
	short := shortenPath(path)
	if info.BaseDir != "" {
		if rel, err := filepath.Rel(info.BaseDir, path); err == nil && rel != "." {
			short = filepath.ToSlash(rel)
		}
	}
	if info.Scope != "" {
		return fmt.Sprintf("%s: %s (%s) %s", display, label, info.Scope, short)
	}
	return fmt.Sprintf("%s: %s %s", display, label, short)
}

func (m *InteractiveMode) resourceCollisionDiagnostics() []string {
	var out []string
	out = append(out, m.collisionDiagnosticsForSkills()...)
	out = append(out, m.collisionDiagnosticsForPrompts()...)
	return out
}

func (m *InteractiveMode) collisionDiagnosticsForSkills() []string {
	groups := map[string][]ResourceSourceInfo{}
	for _, info := range m.resourceSourceInfo {
		if info.ResourceType == "skills" && info.Enabled {
			groups[info.DisplayName()] = append(groups[info.DisplayName()], info)
		}
	}
	winners := map[string]string{}
	for _, s := range m.opts.Skills {
		winners[s.Name] = s.Path
	}
	return m.buildCollisionDiagnostics("skill", groups, winners)
}

func (m *InteractiveMode) collisionDiagnosticsForPrompts() []string {
	groups := map[string][]ResourceSourceInfo{}
	for _, info := range m.resourceSourceInfo {
		if info.ResourceType == "prompts" && info.Enabled {
			groups[info.DisplayName()] = append(groups[info.DisplayName()], info)
		}
	}
	winners := map[string]string{}
	for _, p := range m.promptTemplates {
		winners[p.Name] = p.FilePath
	}
	return m.buildCollisionDiagnostics("prompt", groups, winners)
}

func (m *InteractiveMode) buildCollisionDiagnostics(kind string, groups map[string][]ResourceSourceInfo, winners map[string]string) []string {
	keys := make([]string, 0, len(groups))
	for name, infos := range groups {
		if len(infos) > 1 && winners[name] != "" {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, name := range keys {
		winnerPath := winners[name]
		infos := groups[name]
		var winner *ResourceSourceInfo
		losers := make([]ResourceSourceInfo, 0, len(infos)-1)
		for i := range infos {
			if infos[i].Path == winnerPath {
				winner = &infos[i]
				continue
			}
			losers = append(losers, infos[i])
		}
		if winner == nil || len(losers) == 0 {
			continue
		}
		var msg strings.Builder
		fmt.Fprintf(&msg, "[%s] %q collision: ✓ %s", kind, name, m.formatLoadedResourceRef(winner.Path, name))
		for _, loser := range losers {
			fmt.Fprintf(&msg, "; ✗ %s (skipped)", m.formatLoadedResourceRef(loser.Path, name))
		}
		out = append(out, msg.String())
	}
	return out
}

// shortenPath returns a `~/foo` form for paths under $HOME, otherwise
// the path unchanged. Used in the interactive banner.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	// Any path that starts with home is shortened, so
	// on Windows a / after the home prefix counts as well as \.
	if strings.HasPrefix(p, home) && len(p) > len(home) && os.IsPathSeparator(p[len(home)]) {
		return "~" + p[len(home):]
	}
	return p
}

// debugLog writes to /tmp/wopr-debug.log when WOPR_DEBUG is set. Used for
// diagnosing TUI event flow without polluting the alt-screen.
func debugLog(format string, args ...any) {
	if os.Getenv("WOPR_DEBUG") == "" {
		return
	}
	f, err := os.OpenFile("/tmp/wopr-debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "[%s] ", time.Now().Format("15:04:05.000"))
	_, _ = fmt.Fprintf(f, format+"\n", args...)
}

// showWarning shows a warning toast.
func (m *InteractiveMode) showWarning(msg string) {
	m.showToast("warning", "", msg)
}

// addTerminalInputListener registers a raw terminal input listener.
// Returns an unsubscribe function.
func (m *InteractiveMode) addTerminalInputListener(handler func(string) bool) func() {
	return m.addTerminalInputListenerEntry(terminalInputListener{handler: handler})
}

// addTerminalInputListenerEntry appends listener in registration order and
// returns its unsubscribe function.
func (m *InteractiveMode) addTerminalInputListenerEntry(listener terminalInputListener) func() {
	m.terminalInputMu.Lock()
	m.terminalInputListenerID++
	id := m.terminalInputListenerID
	listener.id = id
	m.terminalInputListeners = append(m.terminalInputListeners, listener)
	m.terminalInputMu.Unlock()
	return func() {
		m.terminalInputMu.Lock()
		defer m.terminalInputMu.Unlock()
		for i, listener := range m.terminalInputListeners {
			if listener.id == id {
				m.terminalInputListeners = append(m.terminalInputListeners[:i], m.terminalInputListeners[i+1:]...)
				break
			}
		}
	}
}

// addKeyPressListener registers a terminal-input listener that sees presses
// only, and is the registration every in-tree consumer with press-once
// semantics should use.
//
// Terminal-input listeners are raw and also see key releases; this filter
// gives consumers with press-once semantics one place that cannot be forgotten.
func (m *InteractiveMode) addKeyPressListener(handler func(string) bool) func() {
	return m.addTerminalInputListener(func(data string) bool {
		if tui.IsKeyRelease(data) {
			return false
		}
		return handler(data)
	})
}
