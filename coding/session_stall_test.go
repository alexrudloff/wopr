package coding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
)

// A file the model edits through the shell (python, sed -i) is progress, so
// the stall nudge doesn't claim nothing changed.
func TestShellChangeToAWatchedFileIsProgress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game.html")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var w fileWatch
	hook := w.afterToolCall(dir)
	hook(context.Background(), "r1", "read", []byte(`{"path":"game.html"}`), agent.AgentToolResult{})

	hook(context.Background(), "b1", "bash", []byte(`{"command":"ls"}`), agent.AgentToolResult{})
	if w.changedFile("b1") {
		t.Fatal("a command that changed nothing counted as a change")
	}
	later := time.Now().Add(time.Second)
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	hook(context.Background(), "b2", "bash", []byte(`{"command":"python3 patch.py"}`), agent.AgentToolResult{})
	if !w.changedFile("b2") {
		t.Fatal("a shell command that changed a watched file wasn't counted as a change")
	}
}
