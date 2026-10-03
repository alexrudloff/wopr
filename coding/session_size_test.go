package coding

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/sessionblob"
)

// No session entry may bloat the file: a shell command writing a
// multi-megabyte binary file, and a tool whose details carry megabytes,
// both leave lines under the limit, and a file written before the limit
// (one line holding megabytes) still loads, trimmed, with the trim reported.
func TestSessionEntriesStayUnderTheLineLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WOPR_HOME", home)
	t.Setenv("HF_TOKEN", "")
	agentDir := filepath.Join(home, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	services, err := NewServices(ServicesOptions{CWD: project, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []scriptedResponse{
		fauxCallWith("bin", "bash", ai.JsonObject{"command": "head -c 3000000 /dev/urandom > out.bin"}),
		fauxReply("done", ai.StopReasonStop, 0),
	}}
	model := &ai.Model{ID: "faux-1", DisplayName: "faux-1", Provider: provider, Capabilities: ai.ModelCapabilities{ContextWindow: 128_000}}
	sess, err := NewSession(services, SessionOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if _, err := sess.Send(context.Background(), "make a binary file"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(project, "out.bin")); err != nil || info.Size() != 3000000 {
		t.Fatalf("out.bin: %v %v", info, err)
	}
	huge := strings.Repeat("x", 5<<20)
	if _, err := sess.Inner().AppendMessage(agent.AgentMessage{ToolResult: &agent.ToolResultMessage{
		Role: "toolResult", ToolCallID: "fake", ToolName: "fake",
		Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "ok"}}, Details: map[string]any{"blob": huge},
	}}); err != nil {
		t.Fatal(err)
	}

	path := sess.Path()
	longest := func() int {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		n, reader := 0, bufio.NewReader(f)
		for {
			line, err := reader.ReadBytes('\n')
			n = max(n, len(line))
			if err != nil {
				return n
			}
		}
	}
	if n := longest(); n > sessionblob.LineLimit {
		t.Fatalf("a session line is %d bytes, over the %d limit", n, sessionblob.LineLimit)
	}

	// A file from before the limit: append a raw multi-megabyte line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"message","id":"old1","parentId":null,"timestamp":"2026-01-01T00:00:00Z","message":{"role":"toolResult","toolCallId":"old","toolName":"bash","content":[{"type":"text","text":"ok"}],"details":{"changes":[{"path":"v.mp4","kind":"edited","diff":"` + huge + `"}]},"isError":false,"timestamp":1}}` + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := icodingagent.NewSessionManagerWithDir(project, filepath.Dir(path)).Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := loaded.TakeTrimmed(); n != 1 {
		t.Fatalf("trimmed %d entries on load, want 1", n)
	}
	entry, ok := loaded.EntryByID("old1")
	if !ok || len(entry.Raw()) > sessionblob.LineLimit || !strings.Contains(string(entry.Raw()), `"text":"ok"`) {
		t.Fatalf("old entry after load: found %v, %d bytes", ok, len(entry.Raw()))
	}
}
