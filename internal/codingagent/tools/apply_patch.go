package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/text"
)

// ─── apply_patch Tool ─────────────────────────────────────────────────────────
//
// apply_patch takes the patch format OpenAI's Codex models are trained on
// ("V4A"): a Begin/End Patch envelope with Add, Delete, and Update File
// sections, where an Update holds hunks of context (" "), removed ("-"), and
// added ("+") lines, optionally located by an "@@ <line>" header. Context is
// matched exactly, then ignoring trailing whitespace, then ignoring
// surrounding whitespace. Every file is checked before any is written.

// ApplyPatchName is the tool's name.
const ApplyPatchName = "apply_patch"

// ApplyPatchTool applies V4A patches.
type ApplyPatchTool struct {
	CWD         string
	Queue       *FileMutationQueue
	Diagnostics *Diagnostics
}

func (t *ApplyPatchTool) Name() string  { return ApplyPatchName }
func (t *ApplyPatchTool) Label() string { return "" }

// ExecutionMode is sequential: a patch may touch several files.
func (t *ApplyPatchTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

func (t *ApplyPatchTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name: ApplyPatchName,
		Description: `Use the apply_patch tool to edit files. The patch language is a stripped-down, file-oriented diff format:

*** Begin Patch
[ one or more file sections ]
*** End Patch

Each file section starts with one of:
*** Add File: <path> - create a new file; every following line starts with +.
*** Delete File: <path> - remove an existing file; nothing follows.
*** Update File: <path> - patch an existing file in place, optionally followed by *** Move to: <new path>.

An Update holds one or more hunks. A hunk may start with "@@ <a line from the file>" (a class or function header) to locate it, then lists lines prefixed with " " (context), "-" (remove), or "+" (add). Show 3 lines of context above and below each change; do not repeat context shared with the previous hunk. Paths are relative to the working directory, never absolute unless the file is outside it.

Example:
*** Begin Patch
*** Update File: src/app.py
@@ def greet():
-    print("Hi")
+    print("Hello, world!")
*** End Patch`,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string", "description": "The entire patch, from *** Begin Patch to *** End Patch"},
			},
			"required": []string{"input"},
		},
	}
}

// v4aHunk is one change inside an Update File section.
type v4aHunk struct {
	anchor   string
	old, new []string
	eof      bool
}

// v4aOp is one file section.
type v4aOp struct {
	kind   byte // 'A' add, 'D' delete, 'U' update
	path   string
	moveTo string
	add    string
	hunks  []v4aHunk
}

// ParsePatch parses a V4A patch.
func ParsePatch(patch string) ([]v4aOp, error) {
	lines := strings.Split(strings.TrimSpace(normalizeToLF(patch)), "\n")
	// A heredoc wrapper (apply_patch <<'EOF' ... EOF) is common; drop it.
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "apply_patch") {
		lines = lines[1:]
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "EOF" {
			lines = lines[:len(lines)-1]
		}
	}
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" || strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return nil, errors.New("the patch must start with *** Begin Patch and end with *** End Patch")
	}
	lines = lines[1 : len(lines)-1]
	var ops []v4aOp
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			op := v4aOp{kind: 'A', path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))}
			var body []string
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "*** "); i++ {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("line %q in Add File %s must start with +", lines[i], op.path)
				}
				body = append(body, lines[i][1:])
			}
			op.add = strings.Join(body, "\n") + "\n"
			ops = append(ops, op)
		case strings.HasPrefix(line, "*** Delete File: "):
			ops = append(ops, v4aOp{kind: 'D', path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			op := v4aOp{kind: 'U', path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))}
			i++
			if i < len(lines) && strings.HasPrefix(lines[i], "*** Move to: ") {
				op.moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to: "))
				i++
			}
			var hunk *v4aHunk
			for ; i < len(lines); i++ {
				l := lines[i]
				if l == "*** End of File" {
					if hunk != nil {
						hunk.eof = true
					}
					continue
				}
				if strings.HasPrefix(l, "*** ") {
					break
				}
				if after, ok := strings.CutPrefix(l, "@@"); ok {
					op.hunks = append(op.hunks, v4aHunk{anchor: strings.TrimSpace(after)})
					hunk = &op.hunks[len(op.hunks)-1]
					continue
				}
				if hunk == nil {
					op.hunks = append(op.hunks, v4aHunk{})
					hunk = &op.hunks[len(op.hunks)-1]
				}
				switch {
				case l == "":
					hunk.old, hunk.new = append(hunk.old, ""), append(hunk.new, "")
				case l[0] == ' ':
					hunk.old, hunk.new = append(hunk.old, l[1:]), append(hunk.new, l[1:])
				case l[0] == '-':
					hunk.old = append(hunk.old, l[1:])
				case l[0] == '+':
					hunk.new = append(hunk.new, l[1:])
				default:
					return nil, fmt.Errorf("line %q in Update File %s must start with a space, -, +, or @@", l, op.path)
				}
			}
			if len(op.hunks) == 0 && op.moveTo == "" {
				return nil, fmt.Errorf("Update File %s has no changes", op.path)
			}
			ops = append(ops, op)
		case strings.TrimSpace(line) == "":
			i++
		default:
			return nil, fmt.Errorf("unexpected line %q; expected *** Add File, *** Delete File, or *** Update File", line)
		}
	}
	if len(ops) == 0 {
		return nil, errors.New("the patch has no file sections")
	}
	return ops, nil
}

// matchers compare a file line with a patch line, strictest first.
var matchers = []func(a, b string) bool{
	func(a, b string) bool { return a == b },
	func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") },
	func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
}

// seek finds want in lines at or after start, trying each matcher in turn;
// eof prefers a match at the end of the file.
func seek(lines, want []string, start int, eof bool) int {
	if len(want) == 0 {
		return start
	}
	for _, match := range matchers {
		if eof {
			if at := len(lines) - len(want); at >= start && linesMatch(lines[at:], want, match) {
				return at
			}
		}
		for at := start; at+len(want) <= len(lines); at++ {
			if linesMatch(lines[at:], want, match) {
				return at
			}
		}
	}
	return -1
}

func linesMatch(lines, want []string, match func(a, b string) bool) bool {
	for i, w := range want {
		if !match(lines[i], w) {
			return false
		}
	}
	return true
}

// applyHunks returns content with hunks applied.
func applyHunks(content string, hunks []v4aHunk, path string) (string, error) {
	lines := strings.Split(content, "\n")
	trailing := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailing {
		lines = lines[:len(lines)-1]
	}
	type replacement struct {
		at, n int
		new   []string
	}
	var reps []replacement
	cursor := 0
	for _, h := range hunks {
		if h.anchor != "" {
			at := seek(lines, []string{h.anchor}, cursor, false)
			if at < 0 {
				return "", fmt.Errorf("could not find the @@ line %q in %s", h.anchor, path)
			}
			cursor = at + 1
		}
		old, repl := h.old, h.new
		at := seek(lines, old, cursor, h.eof)
		if at < 0 && len(old) > 0 && old[len(old)-1] == "" {
			// A trailing blank context line may be the file's final newline.
			old = old[:len(old)-1]
			if len(repl) > 0 && repl[len(repl)-1] == "" {
				repl = repl[:len(repl)-1]
			}
			at = seek(lines, old, cursor, h.eof)
		}
		if at < 0 {
			return "", fmt.Errorf("could not find these lines in %s:\n%s\nRead the file again and resend the patch with context copied exactly", path, strings.Join(h.old, "\n"))
		}
		if len(old) == 0 && h.anchor == "" {
			at = len(lines)
		}
		reps = append(reps, replacement{at: at, n: len(old), new: repl})
		cursor = at + len(old)
	}
	for _, r := range slices.Backward(reps) {

		lines = append(lines[:r.at], append(append([]string{}, r.new...), lines[r.at+r.n:]...)...)
	}
	out := strings.Join(lines, "\n")
	if trailing || out != "" {
		out += "\n"
	}
	return out, nil
}

// patchedFile is one file's planned change.
type patchedFile struct {
	op             v4aOp
	abs, absMoveTo string
	before, after  string
	bom, ending    string
}

func (t *ApplyPatchTool) Execute(ctx context.Context, _ string, rawParams json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		Input string `json:"input"`
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(rawParams, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	ops, err := ParsePatch(in.Input + in.Patch)
	if err != nil {
		return agent.ErrorResult("apply_patch: " + err.Error()), nil
	}
	// Plan every file first so a bad hunk writes nothing.
	var files []patchedFile
	for _, op := range ops {
		f := patchedFile{op: op, abs: resolvePath(t.CWD, op.path)}
		if op.moveTo != "" {
			f.absMoveTo = resolvePath(t.CWD, op.moveTo)
		}
		switch op.kind {
		case 'A':
			f.after = op.add
		case 'D', 'U':
			data, err := os.ReadFile(f.abs)
			if err != nil {
				return agent.ErrorResult(fmt.Sprintf("apply_patch: %s: %s", op.path, nodeFSError(err, "read", ""))), nil
			}
			var decoder utf8StreamDecoder
			bom, content := text.SplitBom(decoder.decode(data, false))
			f.bom, f.ending, f.before = bom, detectLineEnding(content), normalizeToLF(content)
			if op.kind == 'U' {
				if f.after, err = applyHunks(f.before, op.hunks, op.path); err != nil {
					return agent.ErrorResult("apply_patch: " + err.Error()), nil
				}
			}
		}
		files = append(files, f)
	}
	var summary []string
	var diffs []string
	for _, f := range files {
		err := runQueued(ctx, t.Queue, f.abs, func() error {
			switch f.op.kind {
			case 'D':
				return os.Remove(f.abs)
			case 'A':
				if err := os.MkdirAll(filepath.Dir(f.abs), 0o755); err != nil {
					return err
				}
				return os.WriteFile(f.abs, []byte(f.after), 0o644)
			}
			target := f.abs
			if f.absMoveTo != "" {
				target = f.absMoveTo
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
			}
			if err := os.WriteFile(target, []byte(f.bom+restoreLineEndings(f.after, f.ending)), 0o644); err != nil {
				return err
			}
			if target != f.abs {
				return os.Remove(f.abs)
			}
			return nil
		})
		if err != nil {
			return agent.ErrorResult(fmt.Sprintf("apply_patch: %s: %v (earlier files in the patch were applied: %s)", f.op.path, err, strings.Join(summary, ", "))), nil
		}
		shown := f.op.path
		if f.op.moveTo != "" {
			shown += " -> " + f.op.moveTo
		}
		summary = append(summary, string(f.op.kind)+" "+shown)
		if diff, _ := GenerateDiffString(f.before, f.after); diff != "" {
			diffs = append(diffs, diff)
		}
	}
	var content strings.Builder
	content.WriteString("Success. Updated the following files:\n" + strings.Join(summary, "\n"))
	for _, f := range files {
		if f.op.kind == 'D' {
			continue
		}
		path := f.op.path
		if f.op.moveTo != "" {
			path = f.op.moveTo
		}
		if problems := t.Diagnostics.Check(ctx, resolvePath(t.CWD, path), path); problems != "" {
			content.WriteString("\n" + problems)
		}
	}
	return agent.AgentToolResult{Content: content.String(), Details: &EditToolDetails{Diff: strings.Join(diffs, "\n")}}, nil
}
