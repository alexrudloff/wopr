package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
)

// runEdit writes content to a temp file, runs one edit call, and returns the
// result and the file's new content.
func runEdit(t *testing.T, content string, edits []editEntry) (string, *EditToolDetails, string, bool) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(editParams{Path: file, Edits: edits})
	res, err := (&EditTool{CWD: dir, Queue: NewFileMutationQueue()}).Execute(context.Background(), "", args, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	details, _ := res.Details.(*EditToolDetails)
	return res.Content, details, string(after), res.IsError
}

// Edit guards: every edit matches the original content, and any failed edit
// (not found, ambiguous, overlapping) leaves the file untouched.
func TestEditGuards(t *testing.T) {
	text, details, after, isErr := runEdit(t, "alpha\nbeta\ngamma\ndelta\n", []editEntry{{"alpha\n", "ALPHA\n"}, {"gamma\n", "GAMMA\n"}})
	if isErr || after != "ALPHA\nbeta\nGAMMA\ndelta\n" || details == nil || !strings.Contains(details.Diff, "GAMMA") {
		t.Fatalf("disjoint: %q %q", text, after)
	}
	_, _, after, _ = runEdit(t, "foo\nbar\nbaz\n", []editEntry{{"foo\n", "foo bar\n"}, {"bar\n", "BAR\n"}})
	if after != "foo bar\nBAR\nbaz\n" {
		t.Fatalf("edits must match the original content: %q", after)
	}
	for _, c := range []struct {
		content string
		edits   []editEntry
		want    string
	}{
		{"alpha\nbeta\n", []editEntry{{"alpha\n", "ALPHA\n"}, {"missing\n", "M\n"}}, "Could not find"},
		{"one\ntwo\nthree\n", []editEntry{{"one\ntwo\n", "A\n"}, {"two\nthree\n", "B\n"}}, "overlap"},
		{"foo foo foo", []editEntry{{"foo", "bar"}}, "Found 3 occurrences"},
		{"hello\n", []editEntry{{"hello", "hello"}}, "No changes made"},
	} {
		text, _, after, isErr := runEdit(t, c.content, c.edits)
		if !isErr || !strings.Contains(text, c.want) || after != c.content {
			t.Errorf("%q: err=%v %q, file %q", c.want, isErr, text, after)
		}
	}
}

// Line endings and BOM survive an edit.
func TestEditPreservesCRLFAndBOM(t *testing.T) {
	_, _, after, isErr := runEdit(t, "\uFEFFalpha\r\nbeta\r\n", []editEntry{{"beta", "BETA"}})
	if isErr || after != "\uFEFFalpha\r\nBETA\r\n" {
		t.Fatalf("file = %q", after)
	}
}

// Models that send edits as a JSON-encoded string still apply.
func TestEditEditsAsJSONString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	et := &EditTool{CWD: dir, Queue: NewFileMutationQueue()}
	args := json.RawMessage(`{"path":"x.txt","edits":"[{\"oldText\":\"world\",\"newText\":\"wopr\"}]"}`)
	if res, err := et.Execute(context.Background(), "", args, nil); err != nil || res.IsError {
		t.Fatalf("err=%v res=%s", err, res.Content)
	}
	if got, _ := os.ReadFile(path); string(got) != "hello wopr\n" {
		t.Errorf("got %q", got)
	}
}

// A fuzzy match rewrites only the lines it touches.
func TestEditFuzzyMatch(t *testing.T) {
	original := "Title — with “quotes”  \nhard break  \nchange ‘me’\nx²\n"
	_, details, after, isErr := runEdit(t, original, []editEntry{{OldText: "change 'me'", NewText: "changed"}})
	if isErr || after != "Title — with “quotes”  \nhard break  \nchanged\nx²\n" {
		t.Fatalf("file = %q", after)
	}
	if details == nil || strings.Contains(details.Patch, "\n-Title") {
		t.Fatalf("patch must cover only the changed line: %+v", details)
	}
	_, _, after, isErr = runEdit(t, "line one   \nline two  \nline three\n", []editEntry{{"line one\nline two\n", "replaced\n"}})
	if isErr || after != "replaced\nline three\n" {
		t.Fatalf("trailing whitespace: %q", after)
	}
	text, _, _, isErr := runEdit(t, "hello world   \nhello world\n", []editEntry{{"hello world", "replaced"}})
	if !isErr || !strings.Contains(text, "Found 2 occurrences") {
		t.Errorf("fuzzy duplicates must be rejected: %q", text)
	}
}

// Cancelled file tools fail and do not touch the file.
func TestFileToolsAbort(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queue := NewFileMutationQueue()
	for _, c := range []struct {
		tool agent.AgentTool
		args string
	}{
		{&EditTool{CWD: dir, Queue: queue}, `{"path":"f.txt","edits":[{"oldText":"a","newText":"b"}]}`},
		{&WriteTool{CWD: dir, Queue: queue}, `{"path":"f.txt","content":"x"}`},
	} {
		res, err := c.tool.Execute(ctx, "", json.RawMessage(c.args), nil)
		if err != nil || !res.IsError || res.Content != "Operation aborted" {
			t.Errorf("%s: %v %+v", c.tool.Name(), err, res)
		}
	}
	if got, _ := os.ReadFile(file); string(got) != "a\n" {
		t.Fatalf("aborted tools changed the file: %q", got)
	}
}
