package coding

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/internal/text"
)

// War council builds: for a significant change in a Global Thermonuclear
// War session, every council member makes the change in its own git
// worktree (HEAD plus the user's uncommitted changes) and wopr runs the
// tests there. The orchestrator gets each candidate's diff and test result,
// then applies one or merges the best into the user's tree. The user's
// files are untouched until then.

const (
	// buildMinContext is the smallest context window that takes part in a
	// build; a smaller model sits it out.
	buildMinContext = 64_000
	// buildDiffInline is the largest diff shown in full in the result; a
	// larger one is archived for obs_recall.
	buildDiffInline = 12_000
	// buildTestTail is how much of the test output the result keeps.
	buildTestTail = 3_000
	// buildTestLimit bounds wopr's own test run of a candidate.
	buildTestLimit = 10 * time.Minute
)

// councilCandidate is one member's finished change.
type councilCandidate struct {
	id, name, spec string
	// root is the repository the diff applies to.
	root        string
	diff, stat  string
	files       []string
	answer      string
	testCommand string
	tested      bool
	passed      bool
	testTail    string
	duration    time.Duration
	tokens      int
}

// councilBuilds keeps the latest build round's candidates for apply.
type councilBuilds struct {
	mu         sync.Mutex
	candidates []councilCandidate
	// lastTest is the session's latest passing test command.
	lastTest string
}

// initCouncilBuild remembers the latest passing test command, which a build
// candidate is tested with when the orchestrator names none.
func (s *Session) initCouncilBuild() {
	s.agent.AddAfterToolCallHook(func(_ context.Context, _, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
		if command, ok := bashCommand(toolName, args); ok && !result.IsError && testCommandRE.MatchString(command) {
			s.builds.mu.Lock()
			s.builds.lastTest = command
			s.builds.mu.Unlock()
		}
		return agent.AfterToolCallResult{}
	})
}

// git runs git in dir with stdin and returns its trimmed output.
func git(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// councilWorktree is one member's copy of the repository.
type councilWorktree struct {
	root, dir string
	// base is the tree the member starts from: HEAD plus the user's
	// uncommitted changes.
	base string
}

// newCouncilWorktree adds a detached worktree of root at dir and brings over
// the user's uncommitted changes, tracked and untracked, so the member
// starts from what the user sees.
func newCouncilWorktree(ctx context.Context, root, dir string) (*councilWorktree, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if _, err := git(ctx, root, nil, "worktree", "add", "--detach", dir, "HEAD"); err != nil {
		return nil, err
	}
	w := &councilWorktree{root: root, dir: dir}
	fail := func(err error) (*councilWorktree, error) {
		w.remove(context.WithoutCancel(ctx))
		return nil, err
	}
	diff, err := git(ctx, root, nil, "diff", "--binary", "HEAD")
	if err != nil {
		return fail(err)
	}
	if diff != "" {
		if _, err := git(ctx, dir, []byte(diff+"\n"), "apply", "--binary", "--whitespace=nowarn"); err != nil {
			return fail(err)
		}
	}
	untracked, err := git(ctx, root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fail(err)
	}
	for name := range strings.SplitSeq(untracked, "\x00") {
		if name == "" {
			continue
		}
		if err := copyFile(filepath.Join(root, name), filepath.Join(dir, name)); err != nil {
			return fail(err)
		}
	}
	if w.base, err = w.tree(ctx); err != nil {
		return fail(err)
	}
	return w, nil
}

// tree stages everything in the worktree and returns its tree hash.
func (w *councilWorktree) tree(ctx context.Context) (string, error) {
	if _, err := git(ctx, w.dir, nil, "add", "-A"); err != nil {
		return "", err
	}
	return git(ctx, w.dir, nil, "write-tree")
}

// change is the member's diff against the tree it started from, its stat,
// and the files it touched, relative to the repository root.
func (w *councilWorktree) change(ctx context.Context) (diff, stat string, files []string, err error) {
	tree, err := w.tree(ctx)
	if err != nil {
		return "", "", nil, err
	}
	if diff, err = git(ctx, w.dir, nil, "diff", "--binary", w.base, tree); err != nil {
		return "", "", nil, err
	}
	if stat, err = git(ctx, w.dir, nil, "diff", "--stat", w.base, tree); err != nil {
		return "", "", nil, err
	}
	names, err := git(ctx, w.dir, nil, "diff", "--name-only", w.base, tree)
	if err != nil {
		return "", "", nil, err
	}
	for name := range strings.SplitSeq(names, "\n") {
		if name != "" {
			files = append(files, name)
		}
	}
	return diff, stat, files, nil
}

// remove deletes the worktree and its git metadata.
func (w *councilWorktree) remove(ctx context.Context) {
	_, _ = git(ctx, w.root, nil, "worktree", "remove", "--force", w.dir)
	_ = os.RemoveAll(w.dir)
	_, _ = git(ctx, w.root, nil, "worktree", "prune")
}

func copyFile(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil || !info.Mode().IsRegular() {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// applyCandidateDiff applies a candidate's diff to the user's tree at root,
// checking first so a diff that no longer fits changes nothing.
func applyCandidateDiff(ctx context.Context, root, diff string) error {
	if diff == "" {
		return errors.New("the candidate changed nothing")
	}
	if _, err := git(ctx, root, []byte(diff+"\n"), "apply", "--binary", "--check"); err != nil {
		return err
	}
	_, err := git(ctx, root, []byte(diff+"\n"), "apply", "--binary")
	return err
}

// confinedTool refuses a file tool call whose path lies outside the
// candidate's worktree, so a member can't touch the user's files. Bash is
// not confined: it starts in the worktree, at the trust level of the main
// agent's shell.
type confinedTool struct {
	agent.AgentTool
	root, cwd string
}

func (t confinedTool) Execute(ctx context.Context, id string, params json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	for _, path := range toolPaths(t.cwd, t.Name(), params) {
		if !within(t.root, path) {
			return agent.AgentToolResult{}, fmt.Errorf("%s is outside this candidate's copy of the repository (%s): work only inside it", path, t.root)
		}
	}
	return t.AgentTool.Execute(ctx, id, params, onUpdate)
}

// toolPaths are the paths a file tool call reads or writes, resolved as the
// tool resolves them.
func toolPaths(cwd, name string, params json.RawMessage) []string {
	if paths := tools.ChangedPaths(cwd, name, params); paths != nil {
		return paths
	}
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(params, &in)
	return []string{tools.ResolvePath(cwd, cmp.Or(in.Path, "."))}
}

// within reports whether path, with its existing part's symlinks resolved,
// lies inside root.
func within(root, path string) bool {
	resolve := func(p string) string {
		p = filepath.Clean(p)
		var rest []string
		for {
			if real, err := filepath.EvalSymlinks(p); err == nil {
				return filepath.Join(append([]string{real}, rest...)...)
			}
			parent := filepath.Dir(p)
			if parent == p {
				return filepath.Clean(path)
			}
			rest = append([]string{filepath.Base(p)}, rest...)
			p = parent
		}
	}
	rel, err := filepath.Rel(resolve(root), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// councilBuildHost runs a member in its worktree with the full tools,
// confined to it, plus the council's web tools.
type councilBuildHost struct {
	councilHost
	root, cwd string
}

func (h councilBuildHost) Cwd() string { return h.cwd }

func (h councilBuildHost) Tools() []agent.AgentTool {
	s := h.s
	confine := func(t agent.AgentTool) agent.AgentTool { return confinedTool{AgentTool: t, root: h.root, cwd: h.cwd} }
	var out []agent.AgentTool
	for _, tool := range tools.CreateCodingTools(h.cwd, s.services.Settings(), filepath.Join(s.services.AgentDir(), "bin")) {
		switch t := tool.(type) {
		case *tools.BashTool:
			t.HideSessionEnvironment = true
			out = append(out, t)
		case *tools.EditTool:
			// GPT and Codex models edit with apply_patch, as the session's
			// own requests do.
			if usesApplyPatch(h.route.Model) {
				out = append(out, confine(&tools.ApplyPatchTool{CWD: h.cwd, Queue: t.Queue, Diagnostics: t.Diagnostics}))
			} else {
				out = append(out, confine(t))
			}
		default:
			out = append(out, confine(t))
		}
	}
	out = append(out, s.councilWebTools()...)
	if s.efficiency != nil && s.efficiency.pack != nil {
		out = append(out, efficiency.NewRecallTool(s.efficiency.pack))
	}
	return out
}

// councilWorkDir is where a build round's worktrees go: the session's temp
// directory, which exit and the dead-process sweep delete, else its
// runtime root.
func (s *Session) councilWorkDir() string {
	if dir := s.sessionTempDir(); dir != "" {
		return filepath.Join(dir, "council")
	}
	if s.inner != nil {
		if root := efficiency.RuntimeRoot(s.inner.Path(), s.inner.ID()); root != "" {
			return filepath.Join(root, "council")
		}
	}
	return filepath.Join(os.TempDir(), "wopr-council-"+s.ID())
}

// runCouncilBuild has every council member build task in its own worktree
// and test it, and returns the candidates for the orchestrator.
func (s *Session) runCouncilBuild(ctx context.Context, task, testCommand string) (string, string, error) {
	cwd := s.services.CWD()
	root, err := git(ctx, cwd, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("the war council builds in git worktrees, and %s isn't in a git repository: make the change yourself, or ask the council for a plan", cwd)
	}
	rel, _ := filepath.Rel(root, cwd)
	// Worktrees a crashed session left behind are gone from disk by now;
	// drop their metadata.
	_, _ = git(ctx, root, nil, "worktree", "prune")

	members, skipped := s.councilMembers()
	timeout := s.services.Settings().GetWarCouncilBuildTimeout()
	if s.council.timeout > 0 {
		timeout = s.council.timeout
	}
	brief := s.councilBuildBrief(task)
	round := filepath.Join(s.councilWorkDir(), strconv.FormatInt(time.Now().UnixNano(), 36))
	var (
		calls     []councilCall
		worktrees []*councilWorktree
		mu        sync.Mutex
		byName    = map[string]*councilCandidate{}
		trees     = map[string]*councilWorktree{}
	)
	defer func() {
		for _, w := range worktrees {
			w.remove(context.WithoutCancel(ctx))
		}
		_ = os.Remove(round)
	}()
	for i, member := range members {
		model, err := s.routeModel(member.Ref.Provider, member.Ref.Model)
		if err != nil {
			skipped = append(skipped, member.Ref.Spec()+" (unavailable: "+text.Clip(err.Error(), 80)+")")
			continue
		}
		if window := model.Capabilities.ContextWindow; window > 0 && window < buildMinContext {
			skipped = append(skipped, fmt.Sprintf("%s (context window %dK is too small to build)", member.Ref.Spec(), window/1000))
			continue
		}
		w, err := newCouncilWorktree(ctx, root, filepath.Join(round, strconv.Itoa(i+1)))
		if err != nil {
			skipped = append(skipped, member.Ref.Spec()+" (no worktree: "+text.Clip(err.Error(), 120)+")")
			continue
		}
		worktrees = append(worktrees, w)
		route := subagent.Route{
			Model:    model,
			Thinking: ai.ClampThinkingLevel(model, ai.ThinkingMax),
			Provider: member.Ref.Provider,
			Spec:     member.Ref.Spec(),
			Reason:   "war council build",
			Source:   "council",
			Mode:     "council",
		}
		name := cmp.Or(model.DisplayName, member.Ref.Spec())
		memberCwd := filepath.Join(w.dir, rel)
		candidate := &councilCandidate{name: name, spec: route.Spec, root: root}
		byName[name], trees[name] = candidate, w
		calls = append(calls, councilCall{name: name, spec: route.Spec, run: func(ctx context.Context) (string, error) {
			start := time.Now()
			runner := s.councilRunner(councilBuildHost{s: s, route: route, root: w.dir, cwd: memberCwd})
			runner.Finish = func(ctx context.Context, report func(string)) string {
				report("testing")
				command := cmp.Or(testCommand, s.lastTestCommand(), detectTestCommand(memberCwd, w.dir))
				tested, passed, tail := runCandidateTests(ctx, memberCwd, command)
				mu.Lock()
				candidate.testCommand, candidate.tested, candidate.passed, candidate.testTail = command, tested, passed, tail
				mu.Unlock()
				return ""
			}
			id := subagent.NewTaskID(fmt.Sprintf("build\x00%s\x00%d\x00%s", route.Spec, time.Now().UnixNano(), task))
			spec := subagent.Spec{Type: subagent.TypeBuild, Description: "council build · " + name, Brief: brief, Effort: subagent.EffortThorough}
			details, answer, err := runner.Run(ctx, id, spec, nil)
			mu.Lock()
			candidate.duration, candidate.tokens = time.Since(start), details.Tokens
			mu.Unlock()
			return answer, err
		}})
	}
	proposals, late := gatherCouncil(ctx, calls, timeout)
	skipped = append(skipped, late...)

	mu.Lock()
	defer mu.Unlock()
	var candidates []councilCandidate
	for _, p := range proposals {
		c, w := byName[p.name], trees[p.name]
		if c == nil || w == nil {
			continue
		}
		c.answer = p.text
		if c.diff, c.stat, c.files, err = w.change(context.WithoutCancel(ctx)); err != nil {
			skipped = append(skipped, c.name+" (no diff: "+text.Clip(err.Error(), 120)+")")
			continue
		}
		c.id = strconv.Itoa(len(candidates) + 1)
		candidates = append(candidates, *c)
	}
	s.builds.mu.Lock()
	s.builds.candidates = candidates
	s.builds.mu.Unlock()
	return s.councilBuildReport(candidates, skipped)
}

// lastTestCommand is the session's latest passing test command, or "".
func (s *Session) lastTestCommand() string {
	s.builds.mu.Lock()
	defer s.builds.mu.Unlock()
	return s.builds.lastTest
}

// makeTestRE finds a Makefile's test target.
var makeTestRE = regexp.MustCompile(`(?m)^test:`)

// detectTestCommand guesses a project's test command from the files in dir
// or, failing that, root.
func detectTestCommand(dir, root string) string {
	for _, d := range []string{dir, root} {
		exists := func(name string) bool { _, err := os.Stat(filepath.Join(d, name)); return err == nil }
		read := func(name string) string { data, _ := os.ReadFile(filepath.Join(d, name)); return string(data) }
		switch {
		case exists("go.mod"):
			return "go test ./..."
		case exists("Cargo.toml"):
			return "cargo test"
		case strings.Contains(read("package.json"), `"test"`) && !strings.Contains(read("package.json"), "no test specified"):
			return "npm test"
		case makeTestRE.MatchString(read("Makefile")):
			return "make test"
		case exists("pytest.ini") || strings.Contains(read("pyproject.toml"), "pytest") || exists("conftest.py"):
			return "python3 -m pytest -q"
		}
	}
	return ""
}

// runCandidateTests runs command in dir and reports whether it ran, whether
// it passed, and the tail of its output.
func runCandidateTests(ctx context.Context, dir, command string) (tested, passed bool, tail string) {
	if command == "" {
		return false, false, "no test command found; name one with test_command"
	}
	ctx, cancel := context.WithTimeout(ctx, buildTestLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	tail = string(out)
	if len(tail) > buildTestTail {
		tail = "…" + tail[len(tail)-buildTestTail:]
	}
	if ctx.Err() != nil {
		return true, false, tail + "\n(timed out)"
	}
	return true, err == nil, tail
}

// councilBuildBrief is what every building member sees.
func (s *Session) councilBuildBrief(task string) string {
	var b strings.Builder
	b.WriteString("TASK (build this change):\n" + strings.TrimSpace(task) + "\n")
	brief := s.councilBrief(task)
	if _, rest, ok := strings.Cut(brief, "\n\nCONVERSATION SO FAR"); ok {
		b.WriteString("\nCONVERSATION SO FAR" + rest)
	} else if _, rest, ok := strings.Cut(brief, "\n\nFILES THE SESSION"); ok {
		b.WriteString("\nFILES THE SESSION" + rest)
	}
	return b.String()
}

// councilBuildReport is the build round as the orchestrator reads it, and
// the card's one-line title.
func (s *Session) councilBuildReport(candidates []councilCandidate, skipped []string) (string, string, error) {
	passing := 0
	for _, c := range candidates {
		if c.passed {
			passing++
		}
	}
	title := fmt.Sprintf("War council built: %d candidates (%d passing)", len(candidates), passing)
	var b strings.Builder
	b.WriteString(title + "\n")
	if len(candidates) == 0 {
		b.WriteString("No member finished a candidate. Make the change yourself.\n")
	} else {
		b.WriteString("Each candidate is a diff against the user's current files, built independently by another model in its own copy of the repository and tested there by wopr. Compare them; then apply the best with council action \"apply\" and its candidate number, or merge the best parts into the user's files with your edit tools. Say why your choice wins. The user's files are unchanged until you do.\n")
	}
	for _, c := range candidates {
		result := "untested"
		if c.tested {
			result = "tests FAILED"
			if c.passed {
				result = "tests passed"
			}
		}
		fmt.Fprintf(&b, "\n### Candidate %s: %s (%s): %s", c.id, c.name, c.spec, result)
		if c.testCommand != "" {
			fmt.Fprintf(&b, " (`%s`)", c.testCommand)
		}
		fmt.Fprintf(&b, ", %d files, %s, %dK tokens\n", len(c.files), c.duration.Round(time.Second), c.tokens/1000)
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(c.answer))
		if c.stat != "" {
			fmt.Fprintf(&b, "Diff stat:\n```\n%s\n```\n", c.stat)
		}
		switch {
		case c.diff == "":
			b.WriteString("The candidate changed no files.\n")
		case len(c.diff) <= buildDiffInline:
			fmt.Fprintf(&b, "Diff:\n```diff\n%s\n```\n", c.diff)
		default:
			ref := taskHost{s: s}.Archive("council-build:"+c.spec+":"+c.id, c.diff)
			fmt.Fprintf(&b, "Diff: %d KB, too large to show here", len(c.diff)/1024)
			if ref != "" {
				fmt.Fprintf(&b, "; read it with obs_recall {\"id\":%q}", ref)
			}
			b.WriteString(".\n")
		}
		if c.testTail != "" {
			fmt.Fprintf(&b, "Test output (end):\n```\n%s\n```\n", strings.TrimSpace(c.testTail))
		}
	}
	if len(skipped) > 0 {
		b.WriteString("\nNot heard from: " + strings.Join(skipped, "; ") + ".")
	}
	return b.String(), title, nil
}

// applyCouncilCandidate applies a candidate from the latest build round to
// the user's files, snapshotting them first so /undo reverts it.
func (s *Session) applyCouncilCandidate(ctx context.Context, callID, id string) (string, error) {
	s.builds.mu.Lock()
	var candidate *councilCandidate
	for i := range s.builds.candidates {
		if s.builds.candidates[i].id == strings.TrimSpace(id) {
			candidate = &s.builds.candidates[i]
		}
	}
	n := len(s.builds.candidates)
	s.builds.mu.Unlock()
	if candidate == nil {
		if n == 0 {
			return "", errors.New("no council build to apply from: build one with council action \"build\"")
		}
		return "", fmt.Errorf("no candidate %q: the latest build has candidates 1 to %d", id, n)
	}
	var snaps []pendingSnapshot
	for _, f := range candidate.files {
		path := filepath.Join(candidate.root, f)
		snaps = append(snaps, pendingSnapshot{tool: "council", path: path, before: readFileState(path)})
	}
	if err := applyCandidateDiff(ctx, candidate.root, candidate.diff); err != nil {
		return "", fmt.Errorf("candidate %s doesn't apply cleanly to the current files (they changed since the build): %w. Merge it by hand with your edit tools", id, err)
	}
	for _, snap := range snaps {
		if after := readFileState(snap.path); after.exists != snap.before.exists || after.sum() != snap.before.sum() {
			_ = s.recordChange(snap, callID, after)
		}
	}
	return fmt.Sprintf("Applied candidate %s (%s) to %d files: %s", id, candidate.name, len(candidate.files), strings.Join(candidate.files, ", ")), nil
}
