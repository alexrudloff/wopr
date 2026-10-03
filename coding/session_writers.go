package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// General subagents: a task of type general runs with the full tools in the
// session's own working directory. Its tool calls go through the session's
// file hooks (undo snapshots, shell-edit capture, the overwrite guard, safety
// backups), so its changes are the session's. A file a running writer has
// changed is locked to it until it finishes: other writers' and the
// orchestrator's edits to it are refused, naming the holder.
//
// Without a frontend to deliver background results (print, JSON, and RPC
// mode), finished results queue up and the orchestrator's run, when it would
// end, waits for the running background tasks and reads their results
// first, so a final check comes after them.

// fileHooks are the session's tool call hooks that guard and record file
// changes, captured as they are installed so general children can run them.
type fileHooks struct {
	before []agent.BeforeToolCallHook
	after  []agent.AfterToolCallHook
}

// captureFileHooks runs install and records the hooks it added to the
// orchestrator's agent as file hooks.
func (s *Session) captureFileHooks(install func()) {
	before0, after0 := s.agent.ToolCallHooks()
	install()
	before, after := s.agent.ToolCallHooks()
	s.tasks.files.before = append(s.tasks.files.before, before[len(before0):]...)
	s.tasks.files.after = append(s.tasks.files.after, after[len(after0):]...)
}

// fileLocks maps a file to the running writer that changed it.
type fileLocks struct {
	mu     sync.Mutex
	holder map[string]string // path -> task id
	label  map[string]string // task id -> label
}

// claim locks paths to owner, or names the task that holds one of them.
func (l *fileLocks) claim(owner, label string, paths []string) (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range paths {
		if h, ok := l.holder[p]; ok && h != owner {
			return p, h
		}
	}
	if l.holder == nil {
		l.holder, l.label = map[string]string{}, map[string]string{}
	}
	for _, p := range paths {
		l.holder[p] = owner
	}
	l.label[owner] = label
	return "", ""
}

// held names the writer holding one of paths, or "".
func (l *fileLocks) held(paths []string) (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range paths {
		if h, ok := l.holder[p]; ok {
			return p, h
		}
	}
	return "", ""
}

// release frees owner's files.
func (l *fileLocks) release(owner string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p, h := range l.holder {
		if h == owner {
			delete(l.holder, p)
		}
	}
	delete(l.label, owner)
}

func (l *fileLocks) labelOf(owner string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.label[owner]
}

// lockRefusal is the reason an edit of path, held by holder, is refused.
func (l *fileLocks) lockRefusal(cwd, path, holder string) string {
	name := holder
	if label := l.labelOf(holder); label != "" {
		name += " (" + label + ")"
	}
	return fmt.Sprintf("%s is being changed by background task %s, which holds it until it finishes. Don't edit it now: work on other files, or wait for its result.", displayRel(cwd, path), name)
}

// lockedPaths are the files a tool call would change, for the lock: a file
// tool's paths, and the files a shell command visibly writes (redirection,
// tee, and sed -i targets). Other files a command changes are locked once
// it has run.
func lockedPaths(cwd, toolName string, args json.RawMessage) []string {
	if toolName != "bash" {
		return tools.ChangedPaths(cwd, toolName, args)
	}
	return shellWriteTargets(cwd, commandText(args))
}

// shellWriteTargets are the files command writes with a redirection, tee,
// or sed -i, resolved against cwd. A redirection's target may not exist
// yet; sed's are the operands that are existing files, so its script and
// backup suffix are skipped. /dev paths are left out.
func shellWriteTargets(cwd, command string) []string {
	var out []string
	add := func(w string) {
		if w != "" && !strings.HasPrefix(w, "&") && !strings.HasPrefix(w, "/dev/") {
			out = append(out, canonical(tools.ResolvePath(cwd, w)))
		}
	}
	for _, segment := range segmentSplit.Split(command, -1) {
		words := shellWords(segment)
		for i := 0; i < len(words); i++ {
			w := words[i]
			if op := strings.TrimLeft(w, "0123456789&"); strings.HasPrefix(op, ">") {
				if target := strings.TrimLeft(op, ">|"); target != "" {
					add(target)
				} else if i+1 < len(words) {
					i++
					add(words[i])
				}
			}
		}
		if len(words) == 0 {
			continue
		}
		switch filepath.Base(words[0]) {
		case "tee":
			for _, w := range words[1:] {
				if !strings.HasPrefix(w, "-") && !strings.Contains(w, ">") {
					add(w)
				}
			}
		case "sed":
			if !slices.ContainsFunc(words[1:], func(w string) bool { return strings.HasPrefix(w, "-i") || w == "--in-place" }) {
				continue
			}
			for _, w := range words[1:] {
				if path := tools.ResolvePath(cwd, w); !strings.HasPrefix(w, "-") && isRegularFile(path) {
					add(w)
				}
			}
		}
	}
	return out
}

// shellWords splits a simple command into words, honouring single and
// double quotes.
func shellWords(segment string) []string {
	var words []string
	var b strings.Builder
	in, quote := false, rune(0)
	for _, r := range segment {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				words = append(words, b.String())
				b.Reset()
				in = false
			}
		default:
			b.WriteRune(r)
			in = true
		}
	}
	if in {
		words = append(words, b.String())
	}
	return words
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// writerGuard is the orchestrator's before hook: its edits to a file a
// running writer holds are refused.
func (s *Session) writerGuard(_ context.Context, _, toolName string, args json.RawMessage) agent.ToolCallHookResult {
	cwd := s.services.CWD()
	if path, holder := s.tasks.locks.held(lockedPaths(cwd, toolName, args)); holder != "" {
		return agent.ToolCallHookResult{Block: true, Reason: s.tasks.locks.lockRefusal(cwd, path, holder)}
	}
	return agent.ToolCallHookResult{}
}

// Writer returns a general child's tools, with the session's file hooks and
// the file lock for task id.
func (h taskHost) Writer(id string, model *ai.Model) subagent.Writer {
	s := h.s
	cwd := s.services.CWD()
	var out []agent.AgentTool
	for _, tool := range tools.CreateCodingTools(cwd, s.services.Settings(), filepath.Join(s.services.AgentDir(), "bin")) {
		switch t := tool.(type) {
		case *tools.EditTool:
			if usesApplyPatch(model) {
				out = append(out, &tools.ApplyPatchTool{CWD: cwd, Queue: t.Queue, Diagnostics: t.Diagnostics})
			} else {
				out = append(out, t)
			}
		default:
			out = append(out, tool)
		}
	}
	withBackgroundShells(out, s.bgShells)
	out = append(out, s.councilWebTools()...)
	if s.efficiency != nil && s.efficiency.pack != nil {
		out = append(out, efficiency.NewRecallTool(s.efficiency.pack))
	}

	label := id
	if registry := s.Agents(); registry != nil {
		if a, ok := registry.Get(id); ok {
			label = a.Label()
		}
	}
	var mu sync.Mutex
	changed := map[string]bool{}
	lock := func(_ context.Context, _, toolName string, args json.RawMessage) agent.ToolCallHookResult {
		if path, holder := s.tasks.locks.claim(id, label, lockedPaths(cwd, toolName, args)); holder != "" {
			return agent.ToolCallHookResult{Block: true, Reason: s.tasks.locks.lockRefusal(cwd, path, holder)}
		}
		return agent.ToolCallHookResult{}
	}
	// record runs after the file hooks, so a shell command's changes are in
	// its details by then.
	record := func(_ context.Context, _, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
		paths := lockedPaths(cwd, toolName, args)
		if d, ok := result.Details.(*tools.BashDetails); ok && d != nil {
			for _, c := range d.Changes {
				paths = append(paths, tools.ResolvePath(cwd, c.Path))
			}
		}
		if len(paths) == 0 || result.IsError && toolName != "bash" {
			return agent.AfterToolCallResult{}
		}
		// A shell command's changes are locked once made.
		if toolName == "bash" {
			s.tasks.locks.claim(id, label, paths)
		}
		mu.Lock()
		for _, p := range paths {
			changed[p] = true
		}
		mu.Unlock()
		return agent.AfterToolCallResult{}
	}
	files := s.tasks.files
	return subagent.Writer{
		Tools:  out,
		Before: append([]agent.BeforeToolCallHook{lock}, files.before...),
		After:  append(slices.Clone(files.after), record),
		Changed: func() []string {
			mu.Lock()
			defer mu.Unlock()
			var list []string
			for p := range changed {
				list = append(list, displayRel(cwd, p))
			}
			slices.Sort(list)
			return list
		},
	}
}

// displayRel shows path relative to cwd when it is inside it.
func displayRel(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return path
}

// headlessResults queues finished background results while no frontend
// delivers them.
type headlessResults struct {
	mu      sync.Mutex
	pending []agent.AgentMessage
	signal  chan struct{}
}

func (q *headlessResults) push(msg agent.AgentMessage) {
	q.mu.Lock()
	q.pending = append(q.pending, msg)
	if q.signal == nil {
		q.signal = make(chan struct{}, 1)
	}
	signal := q.signal
	q.mu.Unlock()
	select {
	case signal <- struct{}{}:
	default:
	}
}

func (q *headlessResults) take() []agent.AgentMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	msgs := q.pending
	q.pending = nil
	return msgs
}

func (q *headlessResults) wait() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.signal == nil {
		q.signal = make(chan struct{}, 1)
	}
	return q.signal
}

// backgroundWaitTick bounds each wait for a background task, so a task that
// ends without a result (stopped, interrupted) doesn't hold the run.
const backgroundWaitTick = 2 * time.Second

// awaitBackground hands queued background results to the run as
// follow-ups and, when the run would end with background tasks still
// running and no frontend to deliver them later, waits for them.
func (s *Session) awaitBackground(ctx context.Context, turn agent.AgentTurnContext) {
	if s.tasks.deliver.Load() != nil {
		return
	}
	ending := turn.Message != nil && turn.Message.StopReason == ai.StopReasonStop && len(turn.ToolResults) == 0
	for {
		if msgs := s.tasks.headless.take(); len(msgs) > 0 {
			for _, msg := range msgs {
				s.agent.FollowUp(msg)
			}
			return
		}
		registry := s.Agents()
		if !ending || registry == nil || !slices.ContainsFunc(registry.Running(), func(a subagent.Agent) bool { return a.Background }) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-s.tasks.headless.wait():
		case <-time.After(backgroundWaitTick):
		}
	}
}
