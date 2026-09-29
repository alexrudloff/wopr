package coding

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// File snapshots: before write, edit, or apply_patch changes a file, its
// bytes (or its absence) are kept in the session's runtime root, and /undo
// puts them back. Only the files a tool changes are copied; changes made by
// shell commands are not covered.

const (
	// undoMaxBytes skips snapshotting larger files.
	undoMaxBytes = 5 << 20
	// undoMessageType is the custom message that tells the model about an undo.
	undoMessageType = "undo"
	undoLogName     = "log.jsonl"
)

// undoEntry is one line of the change log. A "change" records a file before
// a tool changed it; an "undo" records the file before an undo restored it,
// so the version the undo replaced stays recoverable.
type undoEntry struct {
	Seq    int    `json:"seq"`
	Kind   string `json:"kind"` // "change" | "undo"
	Tool   string `json:"tool,omitempty"`
	Call   string `json:"call,omitempty"`
	Path   string `json:"path"`
	Snap   string `json:"snap,omitempty"` // file name under snapshots/
	Absent bool   `json:"absent,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	// After is the SHA-256 of the file after the change, "" when the change
	// removed it.
	After   string `json:"after,omitempty"`
	Undoes  int    `json:"undoes,omitempty"`
	Prompt  int64  `json:"prompt,omitempty"`
	Skipped string `json:"skipped,omitempty"`
	Time    int64  `json:"time"`
}

// fileState is a file as a snapshot sees it.
type fileState struct {
	exists  bool
	data    []byte
	mode    fs.FileMode
	skipped string
}

func readFileState(path string) fileState {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fileState{}
	}
	if info.Size() > undoMaxBytes {
		return fileState{exists: true, mode: info.Mode().Perm(), skipped: "larger than 5 MB"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{exists: true, mode: info.Mode().Perm(), skipped: err.Error()}
	}
	return fileState{exists: true, data: data, mode: info.Mode().Perm()}
}

func (f fileState) sum() string {
	if !f.exists {
		return ""
	}
	if f.skipped != "" {
		return "skipped"
	}
	h := sha256.Sum256(f.data)
	return hex.EncodeToString(h[:])
}

// fileUndo holds one session's pending snapshots between a tool's before and
// after hooks.
type fileUndo struct {
	mu      sync.Mutex
	pending map[string][]pendingSnapshot // by tool call ID
	// prompt identifies the user prompt the next changes belong to.
	prompt atomic.Int64
}

type pendingSnapshot struct {
	tool   string
	path   string
	before fileState
}

// initUndo snapshots every file write, edit, and apply_patch are about to
// change, and logs the ones they did change.
func (s *Session) initUndo() {
	s.undo.prompt.Store(time.Now().UnixNano())
	s.agent.AddBeforeToolCallHook(func(_ context.Context, callID, toolName string, args json.RawMessage) agent.ToolCallHookResult {
		paths := tools.ChangedPaths(s.services.CWD(), toolName, args)
		if len(paths) == 0 {
			return agent.ToolCallHookResult{}
		}
		var snaps []pendingSnapshot
		for _, path := range paths {
			snaps = append(snaps, pendingSnapshot{tool: toolName, path: path, before: readFileState(path)})
		}
		s.undo.mu.Lock()
		if s.undo.pending == nil {
			s.undo.pending = map[string][]pendingSnapshot{}
		}
		s.undo.pending[callID] = snaps
		s.undo.mu.Unlock()
		return agent.ToolCallHookResult{}
	})
	s.agent.AddAfterToolCallHook(func(_ context.Context, callID, _ string, _ json.RawMessage, _ agent.AgentToolResult) agent.AfterToolCallResult {
		s.undo.mu.Lock()
		snaps := s.undo.pending[callID]
		delete(s.undo.pending, callID)
		s.undo.mu.Unlock()
		for _, snap := range snaps {
			// A failed call can still have changed the file (a fused
			// then_run that failed after the edit), so compare the bytes.
			after := readFileState(snap.path)
			if after.exists == snap.before.exists && after.sum() == snap.before.sum() {
				continue
			}
			_ = s.recordChange(snap, callID, after)
		}
		return agent.AfterToolCallResult{}
	})
}

// undoDir is the session's snapshot directory, or "" when the session is
// not saved.
func (s *Session) undoDir() string {
	if s.inner == nil {
		return ""
	}
	root := efficiency.RuntimeRoot(s.inner.Path(), s.inner.ID())
	if root == "" {
		return ""
	}
	return filepath.Join(root, "snapshots")
}

func (s *Session) recordChange(snap pendingSnapshot, callID string, after fileState) error {
	dir := s.undoDir()
	if dir == "" {
		return nil
	}
	s.undo.mu.Lock()
	defer s.undo.mu.Unlock()
	entry := undoEntry{Kind: "change", Tool: snap.tool, Call: callID, Path: snap.path, Absent: !snap.before.exists,
		Mode: uint32(snap.before.mode), After: after.sum(), Prompt: s.undo.prompt.Load(), Skipped: snap.before.skipped}
	return appendUndoEntry(dir, &entry, snap.before)
}

// appendUndoEntry stores state's bytes (when it has any) and appends entry
// to the log with the next sequence number. The caller holds undo.mu.
func appendUndoEntry(dir string, entry *undoEntry, state fileState) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := readUndoLog(dir)
	if err != nil {
		return err
	}
	entry.Seq = len(entries) + 1
	entry.Time = time.Now().UnixMilli()
	if state.exists && state.skipped == "" {
		entry.Snap = fmt.Sprintf("%06d.bin", entry.Seq)
		if err := os.WriteFile(filepath.Join(dir, entry.Snap), state.data, 0o600); err != nil {
			return err
		}
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, undoLogName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	return errors.Join(werr, f.Close())
}

func readUndoLog(dir string) ([]undoEntry, error) {
	data, err := os.ReadFile(filepath.Join(dir, undoLogName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []undoEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var e undoEntry
		if json.Unmarshal(scanner.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, scanner.Err()
}

// Undo reverts file changes: target "" reverts the latest tool call's
// changes, "prompt" every change since the latest prompt that made one, and
// anything else the latest change to that path. Each revert first saves the
// file as it is, so repeated undos step further back and the replaced
// version stays in the session archive. The model is told at its next turn.
func (s *Session) Undo(target string) ([]icodingagent.UndoChange, error) {
	dir := s.undoDir()
	if dir == "" {
		return nil, errors.New("undo needs a saved session")
	}
	s.undo.mu.Lock()
	out, err := undoIn(dir, s.services.CWD(), target)
	s.undo.mu.Unlock()
	if len(out) > 0 {
		var names []string
		for _, c := range out {
			names = append(names, relativeTo(s.services.CWD(), c.Path))
		}
		s.agent.QueueNextTurn(agent.AgentMessage{Custom: map[string]any{
			"role": agent.RoleCustom, "customType": undoMessageType, "display": false,
			"content":   "[The user undid your change to " + strings.Join(names, ", ") + "; it is back to its earlier contents. Read it again before relying on it.]",
			"timestamp": time.Now().UnixMilli(),
		}})
	}
	return out, err
}

// undoIn picks the changes target names from the log in dir and reverts
// them, newest first. The caller holds undo.mu.
func undoIn(dir, cwd, target string) ([]icodingagent.UndoChange, error) {
	entries, err := readUndoLog(dir)
	if err != nil {
		return nil, err
	}
	undone := map[int]bool{}
	for _, e := range entries {
		if e.Kind == "undo" {
			undone[e.Undoes] = true
		}
	}
	var open []undoEntry // changes not yet undone, oldest first
	for _, e := range entries {
		if e.Kind == "change" && !undone[e.Seq] {
			open = append(open, e)
		}
	}
	if len(open) == 0 {
		return nil, errors.New("nothing to undo")
	}
	latest := open[len(open)-1]
	target = strings.TrimSpace(target)
	var picked []undoEntry
	for _, e := range slices.Backward(open) {
		switch target {
		case "":
			if e.Call == latest.Call {
				picked = append(picked, e)
			}
		case "prompt":
			if e.Prompt == latest.Prompt {
				picked = append(picked, e)
			}
		default:
			if len(picked) == 0 && e.Path == tools.ResolvePath(cwd, target) {
				picked = append(picked, e)
			}
		}
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("no change to %s to undo", target)
	}
	var out []icodingagent.UndoChange
	for _, e := range picked {
		change, err := revertChange(dir, e)
		if err != nil {
			return out, err
		}
		out = append(out, change)
	}
	return out, nil
}

// revertChange saves the file as it is now, then restores it as it was
// before change e. The caller holds undo.mu.
func revertChange(dir string, e undoEntry) (icodingagent.UndoChange, error) {
	change := icodingagent.UndoChange{Path: e.Path, Tool: e.Tool, Deleted: e.Absent}
	if e.Skipped != "" {
		return change, fmt.Errorf("%s wasn't saved before the change (%s)", e.Path, e.Skipped)
	}
	var data []byte
	if !e.Absent {
		var err error
		if data, err = os.ReadFile(filepath.Join(dir, e.Snap)); err != nil {
			return change, fmt.Errorf("the saved copy of %s is missing: %w", e.Path, err)
		}
	}
	now := readFileState(e.Path)
	change.ChangedSince = now.sum() != e.After
	record := undoEntry{Kind: "undo", Tool: e.Tool, Path: e.Path, Undoes: e.Seq, Absent: !now.exists, Mode: uint32(now.mode), Skipped: now.skipped}
	if err := appendUndoEntry(dir, &record, now); err != nil {
		return change, err
	}
	if record.Snap != "" {
		change.Saved = filepath.Join(dir, record.Snap)
	}
	if e.Absent {
		if err := os.Remove(e.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return change, err
		}
		return change, nil
	}
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err != nil {
		return change, err
	}
	mode := fs.FileMode(e.Mode)
	if mode == 0 {
		mode = 0o644
	}
	if err := os.WriteFile(e.Path, data, mode); err != nil {
		return change, err
	}
	return change, os.Chmod(e.Path, mode)
}

func relativeTo(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
