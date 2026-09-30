package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
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

// Research turns with new searches are progress, and fast idle turns under
// the time floor don't nudge; slow ones do.
func TestStallIdleNeedsTimeAndIgnoresNewResearch(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	turn := func(at time.Time, name string, args ai.JsonObject) agent.AgentMessage {
		return agent.AgentMessage{Assistant: &agent.AssistantMessage{Timestamp: at.UnixMilli(), Content: []ai.AssistantContentBlock{
			ai.ToolCall{ID: name + at.String(), Name: name, Arguments: args},
		}}}
	}
	withPrompt := func(turns []agent.AgentMessage) []agent.AgentMessage {
		return append([]agent.AgentMessage{{User: &agent.UserMessage{Timestamp: start.UnixMilli()}}}, turns...)
	}
	never := func(string) bool { return false }

	var research []agent.AgentMessage
	for i := range 12 {
		research = append(research, turn(start.Add(time.Duration(i)*time.Second), "web_search", ai.JsonObject{"query": fmt.Sprint("rde ", i)}))
	}
	if key, _ := stallCheck(withPrompt(research), never, start.Add(time.Minute)); key != "" {
		t.Fatalf("new searches nudged as %q", key)
	}

	var fast, slow []agent.AgentMessage
	for i := range 11 {
		fast = append(fast, turn(start.Add(time.Duration(i)*time.Second), "read", ai.JsonObject{"path": fmt.Sprint("f", i)}))
		slow = append(slow, turn(start.Add(time.Duration(i)*30*time.Second), "read", ai.JsonObject{"path": fmt.Sprint("f", i)}))
	}
	if key, _ := stallCheck(withPrompt(fast), never, start.Add(15*time.Second)); key != "" {
		t.Fatalf("fast idle turns nudged as %q", key)
	}
	if key, _ := stallCheck(withPrompt(slow), never, start.Add(6*time.Minute)); key == "" {
		t.Fatal("slow idle turns didn't nudge")
	}
}
