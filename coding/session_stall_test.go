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

	var reads, fast, slow, afterWait, exploring, oneLong []agent.AgentMessage
	for i := range 11 {
		reads = append(reads, turn(start.Add(time.Duration(i)*30*time.Second), "read", ai.JsonObject{"path": fmt.Sprint("f", i)}))
		fast = append(fast, turn(start.Add(time.Duration(i)*time.Second), "bash", ai.JsonObject{"command": fmt.Sprint("python3 probe.py ", i)}))
		slow = append(slow, turn(start.Add(time.Duration(i)*30*time.Second), "bash", ai.JsonObject{"command": fmt.Sprint("python3 probe.py ", i)}))
		// The first reply comes after a 10-minute war council.
		afterWait = append(afterWait, turn(start.Add(10*time.Minute+time.Duration(i)*5*time.Second), "bash", ai.JsonObject{"command": fmt.Sprint("python3 probe.py ", i)}))
		exploring = append(exploring, turn(start.Add(time.Duration(i)*30*time.Second), "bash", ai.JsonObject{"command": fmt.Sprint("cd app && rg -n handler", i, " src")}))
		// One five-minute command, then fast turns.
		oneLong = append(oneLong, turn(start.Add(5*time.Minute+time.Duration(i)*5*time.Second), "bash", ai.JsonObject{"command": fmt.Sprint("python3 probe.py ", i)}))
	}
	if key, _ := stallCheck(withPrompt(reads), never, start.Add(6*time.Minute)); key != "" {
		t.Fatalf("reads of new files nudged as %q", key)
	}
	if key, _ := stallCheck(withPrompt(afterWait), never, start.Add(11*time.Minute)); key != "" {
		t.Fatalf("time before the first reply counted as idle: %q", key)
	}
	if key, _ := stallCheck(withPrompt(exploring), never, start.Add(6*time.Minute)); key != "" {
		t.Fatalf("read-only shell exploring nudged as %q", key)
	}
	first := turn(start, "bash", ai.JsonObject{"command": "python3 probe.py start"})
	if key, _ := stallCheck(withPrompt(append([]agent.AgentMessage{first}, oneLong...)), never, start.Add(6*time.Minute)); key != "" {
		t.Fatalf("one long command made up the idle time: %q", key)
	}
	if key, _ := stallCheck(withPrompt(fast), never, start.Add(15*time.Second)); key != "" {
		t.Fatalf("fast idle turns nudged as %q", key)
	}
	if key, _ := stallCheck(withPrompt(slow), never, start.Add(6*time.Minute)); key == "" {
		t.Fatal("slow idle turns didn't nudge")
	}
}
