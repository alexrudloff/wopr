package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
)

// hashlineCtx is a tool-call context served by model.
func hashlineCtx(model string) context.Context {
	return agent.WithToolEnvironment(context.Background(), agent.ToolEnvironment{Provider: "p", Model: model})
}

// weakOnly turns hashline on for the model "weak" only.
func weakOnly(_, model string) bool { return model == "weak" }

func writeHashlineFile(t *testing.T, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "f.txt")
}

func hashlineRead(t *testing.T, dir, model string, extra string) string {
	t.Helper()
	tool := &ReadTool{CWD: dir, Hashline: weakOnly}
	res, err := tool.Execute(hashlineCtx(model), "", json.RawMessage(`{"path":"f.txt"`+extra+`}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("read: %v %s", err, res.Content)
	}
	return res.Content
}

func hashlineEdit(t *testing.T, dir string, args string) agent.AgentToolResult {
	t.Helper()
	tool := &EditTool{CWD: dir, Hashline: weakOnly}
	res, err := tool.Execute(hashlineCtx("weak"), "", json.RawMessage(args), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// anchorsOf maps each served line's text to its anchor.
func anchorsOf(t *testing.T, read string) []string {
	t.Helper()
	var anchors []string
	for row := range strings.SplitSeq(read, "\n") {
		if row == "" {
			break // a continuation notice follows
		}
		anchor, _, ok := strings.Cut(row, "|")
		if !ok || !regexp.MustCompile(`^\d+[a-z]{2}$`).MatchString(anchor) {
			t.Fatalf("row %q has no anchor", row)
		}
		anchors = append(anchors, anchor)
	}
	return anchors
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHashlineReadIsPerModel(t *testing.T) {
	dir, _ := writeHashlineFile(t, "alpha\nbeta\n")
	if got := hashlineRead(t, dir, "strong", ""); got != "alpha\nbeta\n" {
		t.Fatalf("strong model read = %q, want the plain file", got)
	}
	got := hashlineRead(t, dir, "weak", "")
	if !regexp.MustCompile(`^1[a-z]{2}\|alpha\n2[a-z]{2}\|beta$`).MatchString(got) {
		t.Fatalf("weak model read = %q, want anchored rows without a phantom last line", got)
	}
}

func TestHashlineEditSingleLineRangeAndDelete(t *testing.T) {
	dir, path := writeHashlineFile(t, "one\ntwo\nthree\nfour\nfive\n")
	a := anchorsOf(t, hashlineRead(t, dir, "weak", ""))
	res := hashlineEdit(t, dir, `{"path":"f.txt","edits":[
		{"anchor":"`+a[0]+`","newText":"ONE"},
		{"anchor":"`+a[1]+`","end":"`+a[2]+`","newText":"TWO-THREE\nextra"},
		{"anchor":"`+a[4]+`","newText":""}]}`)
	if res.IsError {
		t.Fatalf("edit failed: %s", res.Content)
	}
	if got := readFile(t, path); got != "ONE\nTWO-THREE\nextra\nfour\n" {
		t.Fatalf("file = %q", got)
	}
	fresh := anchorsOf(t, hashlineRead(t, dir, "weak", ""))
	if !strings.Contains(res.Content, "Fresh anchors:\n"+fresh[0]+"|ONE") || !strings.Contains(res.Content, fresh[2]+"|extra") {
		t.Fatalf("result lacks the changed lines' fresh anchors: %s", res.Content)
	}
}

func TestHashlineStaleAnchorIsRejectedWithFreshAnchors(t *testing.T) {
	dir, path := writeHashlineFile(t, "l1\nl2\nl3\nl4\n")
	a := anchorsOf(t, hashlineRead(t, dir, "weak", ""))
	if err := os.WriteFile(path, []byte("l0\nl1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := hashlineEdit(t, dir, `{"path":"f.txt","edits":[{"anchor":"`+a[2]+`","newText":"L3"}]}`)
	if !res.IsError || !strings.Contains(res.Content, "stale") {
		t.Fatalf("shifted anchor accepted: %s", res.Content)
	}
	fresh := anchorsOf(t, hashlineRead(t, dir, "weak", ""))
	if !strings.Contains(res.Content, fresh[3]+"|l3") {
		t.Fatalf("stale error lacks fresh anchors: %s", res.Content)
	}
	if got := readFile(t, path); got != "l0\nl1\nl2\nl3\nl4\n" {
		t.Fatalf("rejected edit changed the file: %q", got)
	}
	res = hashlineEdit(t, dir, `{"path":"f.txt","edits":[{"anchor":"99zz","newText":"x"}]}`)
	if !res.IsError || !strings.Contains(res.Content, "past the end") {
		t.Fatalf("out-of-range anchor: %s", res.Content)
	}
}

func TestHashlineCRLFTabsUnicodeAndBOM(t *testing.T) {
	dir, path := writeHashlineFile(t, "\ufefffunc f() {\r\n\tfmt.Println(\"héllo 世界\")\r\n}\r\n")
	read := hashlineRead(t, dir, "weak", "")
	if strings.Contains(read, "\r") || strings.Contains(read, "\ufeff") {
		t.Fatalf("read leaks CR or BOM: %q", read)
	}
	a := anchorsOf(t, read)
	res := hashlineEdit(t, dir, `{"path":"f.txt","edits":[{"anchor":"`+a[1]+`","newText":"\tfmt.Println(\"😀\")"}]}`)
	if res.IsError {
		t.Fatalf("edit failed: %s", res.Content)
	}
	if got := readFile(t, path); got != "\ufefffunc f() {\r\n\tfmt.Println(\"😀\")\r\n}\r\n" {
		t.Fatalf("file = %q, want BOM and CRLF kept", got)
	}
}
