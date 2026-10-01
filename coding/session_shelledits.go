package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/tempfiles"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Shell edits: a shell command (sed -i, a python rewrite, cat > file) can
// change files the file tools never see. Before each command wopr notes what
// it can restore; after it, every file that changed becomes an ordinary /undo
// change with tool "bash", and the bash card shows its diff. The "before"
// bytes come from a copy wopr keeps of every file the model read or wrote,
// or, inside a git work tree, from the index for a tracked file that was
// clean. A new file counts as created when git lists it as untracked or the
// command names it.

const (
	// shellGitTimeout bounds each git call around a shell command; a slow
	// repository skips git for that command.
	shellGitTimeout = 1500 * time.Millisecond
)

// fileCopy is the state of a watched file when wopr last copied it.
type fileCopy struct {
	sum     string // "" when the file didn't exist
	mtime   time.Time
	mode    fs.FileMode
	skipped string
}

// shellEdits keeps the copies and each running command's before state.
type shellEdits struct {
	mu      sync.Mutex
	copies  map[string]fileCopy
	pending map[string]*shellBefore
	gitRoot *string // resolved once; "" outside a work tree
	// recorded holds each change already recorded, by path and the before
	// and after hashes: shell calls running at once all see a file another
	// one changed, and only the first records it.
	recorded map[string]bool
}

// shellBefore is what a shell command started from.
type shellBefore struct {
	watched map[string]fileCopy
	git     map[string]string // repo-relative path -> porcelain status; nil without git
	named   map[string]bool   // named path -> existed before
	// namedCopies are the bytes of existing files the command names that
	// no copy or clean git index covers: a sed -i on a file the model only
	// grepped, outside git.
	namedCopies map[string]fileState
}

// Named-file copies taken before a command that may write: at most this many
// files and bytes in all, each also under undoMaxBytes.
const (
	namedCopyFiles = 20
	namedCopyBytes = 20 << 20
)

// initShellEdits watches the files the model reads and writes, and turns the
// file changes a shell command makes into /undo changes.
func (s *Session) initShellEdits() {
	s.agent.AddBeforeToolCallHook(func(ctx context.Context, callID, toolName string, args json.RawMessage) agent.ToolCallHookResult {
		if c, ok := s.shellCapture(); ok && toolName == "bash" {
			c.before(ctx, callID, commandText(args))
		}
		return agent.ToolCallHookResult{}
	})
	s.agent.AddAfterToolCallHook(func(ctx context.Context, callID, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
		c, ok := s.shellCapture()
		if !ok {
			return agent.AfterToolCallResult{}
		}
		switch toolName {
		case "read":
			var in struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(args, &in) == nil && in.Path != "" {
				c.copyFile(tools.ResolvePath(c.cwd, in.Path))
			}
		case "bash":
			shown := c.after(ctx, callID, commandText(args))
			if len(shown) == 0 {
				return agent.AfterToolCallResult{}
			}
			details := &tools.BashDetails{}
			if d, ok := result.Details.(*tools.BashDetails); ok && d != nil {
				copied := *d
				details = &copied
			}
			details.Changes = shown
			return agent.AfterToolCallResult{Details: details}
		default:
			for _, path := range tools.ChangedPaths(c.cwd, toolName, args) {
				c.copyFile(path)
			}
		}
		return agent.AfterToolCallResult{}
	})
}

// shellCapture binds the capture to this session's /undo store; ok is false
// for a session that isn't saved.
func (s *Session) shellCapture() (shellCapture, bool) {
	dir := s.undoDir()
	return shellCapture{dir: dir, cwd: s.services.CWD(), edits: &s.shell, record: s.recordChange}, dir != ""
}

// shellCapture is the capture for one /undo store and working directory;
// record appends a change to the /undo log.
type shellCapture struct {
	dir, cwd string
	edits    *shellEdits
	record   func(snap pendingSnapshot, callID string, after fileState) error
}

// copyFile stores path's current bytes under their hash and remembers them
// as the file's state.
func (c shellCapture) copyFile(path string) {
	path = canonical(path)
	state := readFileState(path)
	fc := fileCopy{mode: state.mode, skipped: state.skipped}
	if info, err := os.Stat(path); err == nil {
		fc.mtime = info.ModTime()
	}
	if state.exists && state.skipped == "" {
		fc.sum = state.sum()
		blob := filepath.Join(c.dir, "copies", fc.sum)
		if _, err := os.Stat(blob); err != nil {
			if os.MkdirAll(filepath.Dir(blob), 0o700) != nil || writeFileAtomic(blob, state.data) != nil {
				return
			}
		}
	}
	c.edits.mu.Lock()
	if c.edits.copies == nil {
		c.edits.copies = map[string]fileCopy{}
	}
	c.edits.copies[path] = fc
	c.edits.mu.Unlock()
}

// before notes what a shell command starts from.
func (c shellCapture) before(ctx context.Context, callID, command string) {
	c.edits.mu.Lock()
	var stale []string
	for path, fc := range c.edits.copies {
		if info, err := os.Stat(path); err == nil && !info.ModTime().Equal(fc.mtime) {
			stale = append(stale, path)
		}
	}
	c.edits.mu.Unlock()
	// A file edited outside wopr since its copy is copied again, so an undo
	// returns to what the command started from.
	for _, path := range stale {
		c.copyFile(path)
	}
	c.edits.mu.Lock()
	b := &shellBefore{watched: maps.Clone(c.edits.copies), named: map[string]bool{}}
	c.edits.mu.Unlock()
	if root := c.gitRoot(ctx); root != "" {
		b.git, _ = gitStatus(ctx, root)
	}
	writes := !readOnlyCommandRE.MatchString(command)
	var candidates []string
	for _, path := range namedPaths(c.cwd, command) {
		info, err := os.Lstat(path)
		b.named[path] = err == nil
		if _, watched := b.watched[path]; writes && err == nil && info.Mode().IsRegular() && !watched {
			candidates = append(candidates, path)
		}
	}
	// A clean tracked file needs no copy: the index has it.
	tracked := map[string]bool{}
	if root := c.gitRoot(ctx); root != "" && b.git != nil && len(candidates) > 0 {
		var rels []string
		for _, path := range candidates {
			if rel, ok := gitRel(root, path); ok && b.git[rel] == "" {
				rels = append(rels, rel)
			}
		}
		if len(rels) > 0 {
			out, _ := gitOutput(ctx, root, append([]string{"ls-files", "-z", "--"}, rels...)...)
			for rel := range strings.SplitSeq(string(out), "\x00") {
				if rel != "" {
					tracked[canonical(filepath.Join(root, rel))] = true
				}
			}
		}
	}
	total := 0
	for _, path := range candidates {
		if tracked[path] || len(b.namedCopies) >= namedCopyFiles {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || total+int(info.Size()) > namedCopyBytes {
			continue
		}
		state := readFileState(path)
		if state.skipped != "" {
			continue
		}
		if b.namedCopies == nil {
			b.namedCopies = map[string]fileState{}
		}
		b.namedCopies[path] = state
		total += len(state.data)
	}
	c.edits.mu.Lock()
	if c.edits.pending == nil {
		c.edits.pending = map[string]*shellBefore{}
	}
	c.edits.pending[callID] = b
	c.edits.mu.Unlock()
}

// shellChange is one file a shell command changed.
type shellChange struct {
	path   string
	before fileState
}

// after records the files the command changed as /undo changes and returns
// their diffs for the card.
func (c shellCapture) after(ctx context.Context, callID, command string) []tools.ShellFileChange {
	c.edits.mu.Lock()
	b := c.edits.pending[callID]
	delete(c.edits.pending, callID)
	c.edits.mu.Unlock()
	if b == nil {
		return nil
	}
	var changes []shellChange
	handled := map[string]bool{}
	for path, fc := range b.watched {
		handled[path] = true
		if fc.skipped != "" {
			continue // too large to have been copied
		}
		now := readFileState(path)
		if now.exists == (fc.sum != "") && now.sum() == fc.sum {
			continue
		}
		prev := fileState{mode: fc.mode}
		if fc.sum != "" {
			data, err := os.ReadFile(filepath.Join(c.dir, "copies", fc.sum))
			if err != nil {
				continue
			}
			prev = fileState{exists: true, data: data, mode: fc.mode}
		}
		changes = append(changes, shellChange{path: path, before: prev})
	}
	if b.git != nil {
		root := c.gitRoot(ctx)
		if after, err := gitStatus(ctx, root); err == nil {
			for rel, code := range after {
				path := canonical(filepath.Join(root, rel))
				if handled[path] || b.git[rel] != "" {
					continue // watched, or already changed before the command
				}
				handled[path] = true
				if code == "??" {
					changes = append(changes, shellChange{path: path})
					continue
				}
				// A tracked file that was clean: its before is the index.
				data, ok := gitIndexBytes(ctx, root, rel)
				if !ok {
					continue
				}
				mode := fs.FileMode(0o644)
				if info, err := os.Stat(path); err == nil {
					mode = info.Mode().Perm()
				}
				changes = append(changes, shellChange{path: path, before: fileState{exists: true, data: data, mode: mode}})
			}
		}
	}
	for path, prev := range b.namedCopies {
		if handled[path] {
			continue
		}
		handled[path] = true
		if now := readFileState(path); now.exists != prev.exists || now.sum() != prev.sum() {
			changes = append(changes, shellChange{path: path, before: prev})
		}
	}
	for path, existed := range b.named {
		if handled[path] || existed {
			continue
		}
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			handled[path] = true
			changes = append(changes, shellChange{path: path})
		}
	}
	var shown []tools.ShellFileChange
	for _, ch := range dropTempPaths(changes, c.cwd) {
		after := readFileState(ch.path)
		key := ch.path + "\x00" + ch.before.sum() + "\x00" + after.sum()
		c.edits.mu.Lock()
		seen := c.edits.recorded[key]
		if !seen {
			if c.edits.recorded == nil {
				c.edits.recorded = map[string]bool{}
			}
			c.edits.recorded[key] = true
		}
		c.edits.mu.Unlock()
		if seen {
			continue
		}
		if err := c.record(pendingSnapshot{tool: "bash", path: ch.path, before: ch.before}, callID, after); err != nil {
			continue
		}
		c.copyFile(ch.path)
		shown = append(shown, shellFileDiff(c.cwd, ch, after))
	}
	// Duplicates only arise between calls that overlap; once none is
	// running, the same change later (after an undo, say) is new.
	c.edits.mu.Lock()
	if len(c.edits.pending) == 0 {
		c.edits.recorded = nil
	}
	c.edits.mu.Unlock()
	return shown
}

// gitRoot is the work tree the working directory is in, or "".
func (c shellCapture) gitRoot(ctx context.Context) string {
	c.edits.mu.Lock()
	if c.edits.gitRoot != nil {
		root := *c.edits.gitRoot
		c.edits.mu.Unlock()
		return root
	}
	c.edits.mu.Unlock()
	root := ""
	if out, err := gitOutput(ctx, c.cwd, "rev-parse", "--show-toplevel"); err == nil {
		root = canonical(strings.TrimSpace(string(out)))
	}
	c.edits.mu.Lock()
	c.edits.gitRoot = &root
	c.edits.mu.Unlock()
	return root
}

// canonical resolves path's directory through symlinks, so a path reached
// two ways is one key.
// gitRel is path relative to the repo root, when it lies inside it.
func gitRel(root, path string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func canonical(path string) string {
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(parent, filepath.Base(path))
	}
	return path
}

// shellFileDiff is the diff a bash card shows for one changed file.
func shellFileDiff(cwd string, c shellChange, after fileState) tools.ShellFileChange {
	out := tools.ShellFileChange{Path: relativeTo(cwd, c.path)}
	switch {
	case !c.before.exists && after.exists:
		out.Kind = "created"
	case c.before.exists && !after.exists:
		out.Kind = "deleted"
	default:
		out.Kind = "edited"
	}
	if after.skipped != "" || c.before.skipped != "" {
		return out
	}
	out.Diff, _ = tools.GenerateDiffString(string(c.before.data), string(after.data))
	return out
}

// dropTempPaths leaves out files in temp directories, which temp cleanup
// owns, except inside the working directory: a project under /tmp is still
// the project.
func dropTempPaths(changes []shellChange, cwd string) []shellChange {
	sep := string(filepath.Separator)
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	roots := tempfiles.Roots()
	out := changes[:0]
	for _, c := range changes {
		inTemp := false
		for _, root := range roots {
			if strings.HasPrefix(c.path, root+sep) {
				inTemp = true
			}
		}
		if !inTemp || strings.HasPrefix(c.path, cwd+sep) {
			out = append(out, c)
		}
	}
	return out
}

// gitStatus lists the work tree's changed and untracked files, each with its
// two-letter porcelain status.
func gitStatus(ctx context.Context, root string) (map[string]string, error) {
	out, err := gitOutput(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		code, path := entry[:2], entry[3:]
		status[path] = code
		if code[0] == 'R' || code[0] == 'C' {
			i++ // the rename's source path follows
		}
	}
	return status, nil
}

// gitIndexBytes returns a path's staged content, when it fits the snapshot
// limit.
func gitIndexBytes(ctx context.Context, root, rel string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(ctx, shellGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "show", ":"+filepath.ToSlash(rel))
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(stdout, undoMaxBytes+1))
	waitErr := cmd.Wait()
	if err != nil || waitErr != nil || len(data) > undoMaxBytes {
		return nil, false
	}
	return data, true
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, shellGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func commandText(args json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(args, &in)
	return in.Command
}

// pathToken matches a word that could name a file: it has a slash or a dot.
var pathToken = regexp.MustCompile(`[A-Za-z0-9_~@+./-]*[./][A-Za-z0-9_~@+./-]*`)

// cdTarget matches a `cd <dir>` at the start of a command or after ;, &&,
// ||, |, or (.
var cdTarget = regexp.MustCompile(`(?:^|[;&|(]\s*)cd\s+("[^"]+"|'[^']+'|[^\s;&|)]+)`)

// namedPaths resolves the words of a command that could name files, at
// most 200. A relative word resolves against cwd and against every
// directory the command cds into, since `cd app; sed -i … js/x.js` names
// app/js/x.js.
func namedPaths(cwd, text string) []string {
	dirs := []string{cwd}
	for _, m := range cdTarget.FindAllStringSubmatch(text, -1) {
		dir := strings.Trim(m[1], `"'`)
		if dir != "" && dir != "-" {
			dirs = append(dirs, tools.ResolvePath(cwd, dir))
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, word := range pathToken.FindAllString(text, -1) {
		// A trailing dot ends a sentence; a leading one is ./ or a dotfile.
		word = strings.TrimRight(word, ".")
		if word == "" || strings.HasPrefix(word, "-") || strings.Contains(word, "://") {
			continue
		}
		bases := dirs
		if filepath.IsAbs(word) || strings.HasPrefix(word, "~") {
			bases = dirs[:1]
		}
		for _, base := range bases {
			path := canonical(tools.ResolvePath(base, word))
			if !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
			if len(out) == 200 {
				return out
			}
		}
	}
	return out
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
