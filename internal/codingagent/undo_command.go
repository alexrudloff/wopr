package codingagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// undoCommand is /undo: it reverts the latest file change a tool made, or
// with an argument the latest change to that path ("prompt" reverts every
// change since the latest prompt that made one).
func (m *InteractiveMode) undoCommand(args string) error {
	session, ok := m.opts.SessionHandle.(interface {
		Undo(target string) ([]UndoChange, error)
	})
	if !ok {
		m.showFlash("Undo is unavailable in this session")
		return nil
	}
	if m.runStreaming() {
		m.showFlash("Stop the current reply first (esc), then /undo")
		return nil
	}
	changes, err := session.Undo(args)
	if len(changes) > 0 {
		cwd := m.opts.CWD
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		m.showFlash(undoSummary(changes, cwd))
	}
	if err != nil {
		m.showError("Undo: " + err.Error())
	}
	return nil
}

// undoSummary names what an undo reverted, and where a version the user or a
// shell command made in between was kept. Paths are relativized against the
// session cwd for display only.
func undoSummary(changes []UndoChange, cwd string) string {
	name := func(path string) string {
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
		return path
	}
	var parts []string
	for _, c := range changes {
		verb := "Restored "
		if c.Deleted {
			verb = "Removed "
		}
		parts = append(parts, verb+name(c.Path))
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s (undid %s)", strings.Join(parts, ", "), changes[0].Tool)
	for _, c := range changes {
		if c.ChangedSince && c.Saved != "" {
			fmt.Fprintf(&text, ". %s had changed since; that version is saved at %s", name(c.Path), c.Saved)
		}
	}
	return text.String()
}
