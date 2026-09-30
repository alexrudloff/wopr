// Package tempfiles finds the temp files a wopr session creates and deletes
// them once nothing needs them. Each shell command gets a per-session TMPDIR;
// files written into a temp root by the write and edit tools, and top-level
// temp entries a shell command creates, are recorded in a registry under the
// agent directory with the session and the owning process. They are deleted
// when a compaction leaves them unreferenced, when the session is left or the
// process exits, and at the next start when their owning process is gone.
package tempfiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// RegistryFileName is the registry under the agent directory.
const RegistryFileName = "temp-files.json"

// recentWindow keeps an entry modified or read this recently, when a trigger
// honors recency: the model may still be using it.
const recentWindow = 5 * time.Minute

// Entry is one tracked temp file or directory.
type Entry struct {
	Path      string `json:"path"`
	SessionID string `json:"sessionId"`
	PID       int    `json:"pid"`
	// ProcessStart is the owning process's start time, so a reused PID never
	// counts as the owner.
	ProcessStart string `json:"processStart"`
}

// Tracker records and cleans the temp files of one wopr process.
type Tracker struct {
	registry string
	roots    []string
	pid      int
	start    string
	// baseline holds each root's entries when the process started; they are
	// never attributed to a session.
	baseline map[string]map[string]bool

	mu        sync.Mutex
	snapshots map[string]map[string]map[string]bool // tool call ID -> root -> names
	// protected are working directories: a project under a temp root is
	// never temp material, so nothing inside one, and no directory holding
	// one, is recorded.
	protected []string
}

// Protect keeps dir, everything inside it, and every directory holding it
// out of the records.
func (t *Tracker) Protect(dir string) {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !slices.Contains(t.protected, dir) {
		t.protected = append(t.protected, dir)
	}
}

// isProtected reports whether path is a protected directory, inside one,
// or holds one.
func (t *Tracker) isProtected(path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	sep := string(filepath.Separator)
	for _, dir := range t.protected {
		if path == dir || strings.HasPrefix(path, dir+sep) || strings.HasPrefix(dir, path+sep) {
			return true
		}
	}
	return false
}

// New returns a tracker for this process, recording into agentDir.
func New(agentDir string) *Tracker {
	t := &Tracker{
		registry:  filepath.Join(agentDir, RegistryFileName),
		roots:     Roots(),
		pid:       os.Getpid(),
		snapshots: map[string]map[string]map[string]bool{},
	}
	t.start, _ = processStart(t.pid)
	t.baseline = t.list()
	return t
}

// Roots are the system temp directories, resolved: /tmp and os.TempDir().
func Roots() []string {
	var roots []string
	for _, dir := range []string{"/tmp", os.TempDir()} {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil || slices.Contains(roots, resolved) {
			continue
		}
		roots = append(roots, resolved)
	}
	return roots
}

// SessionDir is the per-session TMPDIR shell commands use.
func SessionDir(sessionID string) string {
	return filepath.Join(os.TempDir(), "wopr-"+sessionID)
}

// list reads the top level of each root.
func (t *Tracker) list() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, root := range t.roots {
		names := map[string]bool{}
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			names[e.Name()] = true
		}
		out[root] = names
	}
	return out
}

// BeforeCommand snapshots the roots before a shell command.
func (t *Tracker) BeforeCommand(callID string) {
	snapshot := t.list()
	t.mu.Lock()
	t.snapshots[callID] = snapshot
	t.mu.Unlock()
}

// AfterCommand records the entries the command created: new since its
// snapshot, absent when the process started, owned by the user, and named
// in text (the command and its output). A new entry nothing names may
// belong to another program writing to the same directory, so it is left
// alone.
func (t *Tracker) AfterCommand(callID, sessionID, text string) {
	t.mu.Lock()
	before, ok := t.snapshots[callID]
	delete(t.snapshots, callID)
	t.mu.Unlock()
	if !ok {
		return
	}
	var created []string
	for root, names := range t.list() {
		for name := range names {
			if before[root][name] || t.baseline[root][name] {
				continue
			}
			path := filepath.Join(root, name)
			if strings.Contains(text, name) && owned(path) {
				created = append(created, path)
			}
		}
	}
	t.record(sessionID, created...)
}

// RecordPath records a file the write or edit tool wrote, when it is inside
// a temp root.
func (t *Tracker) RecordPath(sessionID, path string) {
	if resolved, ok := t.inRoot(path); ok {
		t.record(sessionID, resolved)
	}
}

// inRoot resolves path's parent and reports the path when it lies inside a
// root and is not the root itself.
func (t *Tracker) inRoot(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return "", false
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	for _, root := range t.roots {
		if resolved != root && strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return resolved, true
		}
	}
	return "", false
}

func (t *Tracker) record(sessionID string, paths ...string) {
	paths = slices.DeleteFunc(paths, t.isProtected)
	if len(paths) == 0 || sessionID == "" {
		return
	}
	_ = t.update(func(entries []Entry) []Entry {
		for _, path := range paths {
			if !slices.ContainsFunc(entries, func(e Entry) bool { return e.Path == path }) {
				entries = append(entries, Entry{Path: path, SessionID: sessionID, PID: t.pid, ProcessStart: t.start})
			}
		}
		return entries
	})
}

// CleanUnreferenced deletes the session's entries that text (the summary
// and kept tail after a compaction) names neither by path nor by file name,
// skipping recently used ones and the session's TMPDIR.
func (t *Tracker) CleanUnreferenced(sessionID, text string) int {
	dir, _ := t.inRoot(SessionDir(sessionID))
	return t.clean(true, func(e Entry) bool {
		return e.SessionID == sessionID && e.Path != dir &&
			!strings.Contains(text, e.Path) && !strings.Contains(text, filepath.Base(e.Path))
	}, "")
}

// CleanSession deletes the session's entries and TMPDIR, as when the user
// leaves it. recent keeps recently used entries tracked for a later trigger.
func (t *Tracker) CleanSession(sessionID string, recent bool) int {
	return t.clean(recent, func(e Entry) bool { return e.SessionID == sessionID }, sessionID)
}

// CleanProcess deletes every entry this process owns, at exit.
func (t *Tracker) CleanProcess(sessionIDs ...string) int {
	n := t.clean(false, func(e Entry) bool { return e.PID == t.pid && e.ProcessStart == t.start }, "")
	for _, id := range sessionIDs {
		if removeSafely(t.roots, SessionDir(id)) {
			n++
		}
	}
	return n
}

// SweepDead deletes the entries, and the session TMPDIRs, of processes that
// are no longer running.
func (t *Tracker) SweepDead() int {
	alive := map[string]bool{}
	dead := func(e Entry) bool {
		key := fmt.Sprintf("%d %s", e.PID, e.ProcessStart)
		live, seen := alive[key]
		if !seen {
			start, ok := processStart(e.PID)
			live = ok && start == e.ProcessStart
			alive[key] = live
		}
		return !live
	}
	var sessions []string
	n := t.clean(false, func(e Entry) bool {
		if dead(e) {
			sessions = append(sessions, e.SessionID)
			return true
		}
		return false
	}, "")
	for _, id := range slices.Compact(slices.Sorted(slices.Values(sessions))) {
		if removeSafely(t.roots, SessionDir(id)) {
			n++
		}
	}
	return n
}

// clean deletes the selected entries and drops them, and any that no
// longer exist, from the registry; an entry skipped as recent stays. When
// sessionID is set, that session's TMPDIR goes too.
func (t *Tracker) clean(recent bool, selected func(Entry) bool, sessionID string) int {
	removed := 0
	_ = t.update(func(entries []Entry) []Entry {
		kept := entries[:0]
		for _, e := range entries {
			if !selected(e) {
				kept = append(kept, e)
				continue
			}
			if _, err := os.Lstat(e.Path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if recent && usedRecently(e.Path, time.Now()) {
				kept = append(kept, e)
				continue
			}
			if t.isProtected(e.Path) {
				// A working directory, or one holding it: never temp material.
				continue
			}
			if removeSafely(t.roots, e.Path) {
				removed++
				continue
			}
			kept = append(kept, e)
		}
		return kept
	})
	if sessionID != "" && removeSafely(t.roots, SessionDir(sessionID)) {
		removed++
	}
	return removed
}

// removeSafely deletes path when it lies inside a root (after resolving its
// parent), is not a root, and is owned by the user. A symlink is removed
// itself, never followed. It reports whether something was deleted.
func removeSafely(roots []string, path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return false
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	inside := slices.ContainsFunc(roots, func(root string) bool {
		return resolved != root && strings.HasPrefix(resolved, root+string(filepath.Separator))
	})
	if !inside || !owned(resolved) {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(resolved) == nil
	}
	return os.RemoveAll(resolved) == nil
}

// usedRecently reports a modification or access within recentWindow.
func usedRecently(path string, now time.Time) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	latest := info.ModTime()
	if access := accessTime(path); access.After(latest) {
		latest = access
	}
	return now.Sub(latest) < recentWindow
}

// update rewrites the registry under its file lock.
func (t *Tracker) update(fn func([]Entry) []Entry) error {
	if err := os.MkdirAll(filepath.Dir(t.registry), 0o700); err != nil {
		return err
	}
	lock := flock.New(t.registry + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	var entries []Entry
	if data, err := os.ReadFile(t.registry); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	entries = fn(entries)
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.registry, append(data, '\n'), 0o600)
}

var (
	processOnce    sync.Once
	processTracker *Tracker
	sessionsMu     sync.Mutex
	sessionsUsed   []string
)

// Process returns this process's tracker, creating it on first use and
// sweeping the entries of dead processes in the background.
func Process(agentDir string) *Tracker {
	processOnce.Do(func() {
		processTracker = New(agentDir)
		go processTracker.SweepDead()
	})
	return processTracker
}

// Use notes a session whose TMPDIR this process may have created, so exit
// removes it.
func Use(sessionID string) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if !slices.Contains(sessionsUsed, sessionID) {
		sessionsUsed = append(sessionsUsed, sessionID)
	}
}

// ExitCleanup deletes everything this process tracked. Exit paths that skip
// the normal shutdown (signals) call it; it does nothing when no tracker
// was created.
func ExitCleanup() {
	if processTracker == nil {
		return
	}
	sessionsMu.Lock()
	ids := slices.Clone(sessionsUsed)
	sessionsMu.Unlock()
	processTracker.CleanProcess(ids...)
}
