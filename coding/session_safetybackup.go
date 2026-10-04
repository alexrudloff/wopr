package coding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Safety backups: before a shell command that rewrites or discards git
// history, or opens a SQLite database with a -wal or -shm file beside it,
// wopr copies what the command could destroy and tells the model where.
// Backups live with the session (its runtime directory), so compaction and
// temp-file cleanup never remove them; an unsaved session keeps them under
// the system temp directory.

// backupBudget caps the bytes one session's backups may use.
const backupBudget = 200 << 20

// gitDestructive matches the git subcommands (after `git` and its global
// options) that rewrite or discard history, refs, or uncommitted work.
var gitDestructive = regexp.MustCompile(`^(?:filter-branch|filter-repo|rebase|reset\s.*--hard|reset\s+--hard|push\s.*(?:--force|\s-f\b|--force-with-lease|\+)|clean\s.*-[a-zA-Z]*f|checkout\s.*\s--\s|checkout\s+--\s|checkout\s+\.(?:\s|$)|restore\b|branch\s.*-D\b|branch\s+-D\b|reflog\s+(?:expire|delete)|gc\s.*--prune|update-ref\s+-d|stash\s+(?:drop|clear))`)

// gitGlobal strips git's global options before the subcommand, keeping a
// -C directory.
var gitGlobal = regexp.MustCompile(`^(?:-C\s+("[^"]+"|'[^']+'|\S+)|-c\s+\S+|--[a-z-]+(?:=\S+)?|-[pP])\s*`)

// sqliteExt marks the files a database command may name.
var sqliteExt = regexp.MustCompile(`(?i)\.(?:db|sqlite|sqlite3|db3)$`)

// segmentSplit splits a command line into simple commands.
var segmentSplit = regexp.MustCompile(`&&|\|\||[;|\n]`)

type safetyBackup struct {
	mu      sync.Mutex
	used    int64
	last    map[string]string // repo root -> state hash of its last backup
	lastAt  map[string]string // repo root -> that backup's path
	pending map[string][]string
}

// initSafetyBackup installs the before/after hooks.
func (s *Session) initSafetyBackup() {
	b := &safetyBackup{last: map[string]string{}, lastAt: map[string]string{}, pending: map[string][]string{}}
	s.agent.AddBeforeToolCallHook(func(ctx context.Context, callID, toolName string, args json.RawMessage) agent.ToolCallHookResult {
		if toolName != "bash" {
			return agent.ToolCallHookResult{}
		}
		if notes := b.before(ctx, s.backupDir(), s.services.CWD(), commandText(args)); len(notes) > 0 {
			b.mu.Lock()
			b.pending[callID] = notes
			b.mu.Unlock()
		}
		return agent.ToolCallHookResult{}
	})
	s.agent.AddAfterToolCallHook(func(_ context.Context, callID, _ string, _ json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
		b.mu.Lock()
		notes := b.pending[callID]
		delete(b.pending, callID)
		b.mu.Unlock()
		if len(notes) == 0 {
			return agent.AfterToolCallResult{}
		}
		content := result.Content + "\n\n" + strings.Join(notes, "\n")
		return agent.AfterToolCallResult{Content: &content}
	})
}

// backupDir is where this session's backups go.
func (s *Session) backupDir() string {
	if s.inner != nil {
		if root := efficiency.RuntimeRoot(s.inner.Path(), s.inner.ID()); root != "" {
			return filepath.Join(root, "backups")
		}
	}
	id := "unsaved"
	if s.inner != nil && s.inner.ID() != "" {
		id = s.inner.ID()
	}
	return filepath.Join(os.TempDir(), "wopr-backups-"+id)
}

// before backs up what command could destroy and returns one note per
// backup (or per skipped one).
func (b *safetyBackup) before(ctx context.Context, dir, cwd, command string) []string {
	var notes []string
	seenRepo := map[string]bool{}
	here := cwd
	for _, seg := range segmentSplit.Split(command, -1) {
		seg = strings.TrimSpace(seg)
		fields := strings.Fields(seg)
		if len(fields) >= 2 && fields[0] == "cd" {
			here = tools.ResolvePath(here, strings.Trim(fields[1], `"'`))
			continue
		}
		rest, ok := strings.CutPrefix(seg, "git ")
		if !ok {
			continue
		}
		repoDir := here
		for {
			m := gitGlobal.FindStringSubmatchIndex(rest)
			if m == nil {
				break
			}
			if m[2] >= 0 {
				repoDir = tools.ResolvePath(here, strings.Trim(rest[m[2]:m[3]], `"'`))
			}
			rest = rest[m[1]:]
		}
		if !gitDestructive.MatchString(rest) {
			continue
		}
		root := gitTop(ctx, repoDir)
		if root == "" || seenRepo[root] {
			continue
		}
		seenRepo[root] = true
		if note := b.backupRepo(ctx, dir, root); note != "" {
			notes = append(notes, note)
		}
	}
	if strings.Contains(command, "sqlite") {
		for _, path := range namedPaths(cwd, command) {
			if note := b.backupDatabase(dir, path); note != "" {
				notes = append(notes, note)
			}
		}
	}
	return notes
}

func gitTop(ctx context.Context, dir string) string {
	out, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// backupRepo bundles every ref of root and saves its uncommitted and
// untracked work, unless nothing changed since the last backup of root.
func (b *safetyBackup) backupRepo(ctx context.Context, dir, root string) string {
	refs, _ := gitOutput(ctx, root, "for-each-ref", "--format=%(objectname) %(refname)")
	refs = withoutBackupRefs(refs)
	diff, _ := gitOutput(ctx, root, "diff", "HEAD", "--binary")
	untracked, _ := gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	h := sha256.New()
	h.Write(refs)
	h.Write(diff)
	h.Write(untracked)
	state := hex.EncodeToString(h.Sum(nil))
	b.mu.Lock()
	if b.last[root] == state {
		path := b.lastAt[root]
		b.mu.Unlock()
		return fmt.Sprintf("Backup of git repo %s (unchanged since) is at %s.", root, path)
	}
	b.mu.Unlock()

	size := dirSize(filepath.Join(root, ".git")) + int64(len(diff))
	for rel := range strings.SplitSeq(string(untracked), "\x00") {
		if info, err := os.Stat(filepath.Join(root, rel)); rel != "" && err == nil {
			size += info.Size()
		}
	}
	if !b.reserve(size) {
		return fmt.Sprintf("No backup of git repo %s: it would exceed this session's %d MB backup budget.", root, backupBudget>>20)
	}
	dest := filepath.Join(dir, time.Now().Format("150405.000")+"-"+filepath.Base(root))
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return ""
	}
	var saved []string
	bundle := filepath.Join(dest, "repo.bundle")
	if len(refs) > 0 {
		if _, err := gitOutput(ctx, root, "bundle", "create", bundle, "--all"); err == nil {
			saved = append(saved, "repo.bundle (every ref; restore with git fetch "+bundle+" '+refs/*:refs/wopr-backup/*')")
		}
		if ns := keepRefs(ctx, root, refs, time.Now().Format("20060102-150405")); ns != "" {
			saved = append(saved, "the old refs inside the repo under "+ns+" (the old commits stay reachable: git log "+ns+"heads/<branch>; restore a branch with git reset --hard "+ns+"heads/<branch>; if the point was to purge history, drop them with git for-each-ref --format='delete %(refname)' "+ns+" | git update-ref --stdin)")
		}
	}
	if len(diff) > 0 && os.WriteFile(filepath.Join(dest, "uncommitted.patch"), diff, 0o600) == nil {
		saved = append(saved, "uncommitted.patch (git apply)")
	}
	if n := copyUntracked(root, filepath.Join(dest, "untracked"), untracked); n > 0 {
		saved = append(saved, fmt.Sprintf("untracked/ (%d files)", n))
	}
	if len(saved) == 0 {
		return ""
	}
	b.mu.Lock()
	b.last[root], b.lastAt[root] = state, dest
	b.mu.Unlock()
	return fmt.Sprintf("Backup of git repo %s saved at %s before this command: %s.", root, dest, strings.Join(saved, ", "))
}

// keepRefs copies root's refs under refs/wopr-backup/<stamp>/ inside the
// repo, so a history rewrite leaves the old commits reachable where the
// repo's own tools (and anything checking its objects) can see them. It
// returns the namespace, or "" when nothing was kept.
func keepRefs(ctx context.Context, root string, refs []byte, stamp string) string {
	ns := "refs/wopr-backup/" + stamp + "/"
	var in strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(string(refs)), "\n") {
		sha, name, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(name, "refs/") {
			continue
		}
		fmt.Fprintf(&in, "create %s%s %s\n", ns, strings.TrimPrefix(name, "refs/"), sha)
	}
	if in.Len() == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, shellGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "update-ref", "--stdin")
	cmd.Stdin = strings.NewReader(in.String())
	if cmd.Run() != nil {
		return ""
	}
	return ns
}

// withoutBackupRefs drops refs/wopr-backup/ lines from a for-each-ref
// listing, so kept refs neither change a repo's state nor get kept again.
func withoutBackupRefs(refs []byte) []byte {
	var out []byte
	for _, line := range bytes.SplitAfter(refs, []byte("\n")) {
		if !bytes.Contains(line, []byte(" refs/wopr-backup/")) {
			out = append(out, line...)
		}
	}
	return out
}

// backupDatabase copies a SQLite database and its -wal and -shm files when
// either exists: opening the database can checkpoint and delete them.
func (b *safetyBackup) backupDatabase(dir, path string) string {
	if !sqliteExt.MatchString(path) {
		return ""
	}
	var files []string
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Mode().IsRegular() {
			files = append(files, path+suffix)
		}
	}
	info, err := os.Stat(path)
	if len(files) == 0 || err != nil || !info.Mode().IsRegular() {
		return ""
	}
	files = append([]string{path}, files...)
	var size int64
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			size += fi.Size()
		}
	}
	if !b.reserve(size) {
		return fmt.Sprintf("No backup of database %s: it would exceed this session's %d MB backup budget.", path, backupBudget>>20)
	}
	dest := filepath.Join(dir, time.Now().Format("150405.000")+"-"+filepath.Base(path))
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return ""
	}
	var names []string
	for _, f := range files {
		if copyFilePlain(f, filepath.Join(dest, filepath.Base(f))) == nil {
			names = append(names, filepath.Base(f))
		}
	}
	return fmt.Sprintf("Backup of database %s saved at %s before this command: %s.", path, dest, strings.Join(names, ", "))
}

// reserve takes size bytes from the session's budget.
func (b *safetyBackup) reserve(size int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+size > backupBudget {
		return false
	}
	b.used += size
	return true
}

// copyUntracked copies the NUL-separated untracked files under root into
// dest, keeping their paths.
func copyUntracked(root, dest string, list []byte) int {
	n := 0
	for rel := range strings.SplitSeq(string(list), "\x00") {
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, rel)
		if os.MkdirAll(filepath.Dir(target), 0o700) == nil && copyFilePlain(filepath.Join(root, rel), target) == nil {
			n++
		}
	}
	return n
}

func copyFilePlain(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
