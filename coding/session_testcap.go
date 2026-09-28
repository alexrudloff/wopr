package coding

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/agent"
)

// Test re-run cap: once a test command has passed and nothing has changed
// a file since, running the same command again only repeats the answer. The
// call is answered with a note instead of re-running the suite.

var (
	testCommandRE = regexp.MustCompile(`(^|[;&|(]\s*|\s)(go test|pytest|py\.test|python3? -m (pytest|unittest)|npm (run )?test|yarn test|pnpm test|cargo test|make (test|check)|bun test|deno test|mvn test|gradle test|ctest|rspec|jest|vitest)\b`)
	// readOnlyCommandRE matches shell commands that only look.
	readOnlyCommandRE = regexp.MustCompile(`^\s*(cd\s+\S+\s*(&&|;)\s*)*(cat|ls|grep|rg|head|tail|wc|find|sed -n|awk|git (diff|status|log|show)|pwd|echo|stat|file|tree|nl|less|diff)\b`)
)

// testCap tracks the test commands that passed since the last file change.
type testCap struct {
	mu    sync.Mutex
	green map[string]bool
}

func bashCommand(toolName string, args json.RawMessage) (string, bool) {
	if toolName != "bash" {
		return "", false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &in) != nil {
		return "", false
	}
	return strings.Join(strings.Fields(in.Command), " "), true
}

// before answers a repeated green test command without running it.
func (c *testCap) before(_ context.Context, _, toolName string, args json.RawMessage) agent.ToolCallHookResult {
	command, ok := bashCommand(toolName, args)
	if !ok || !testCommandRE.MatchString(command) {
		return agent.ToolCallHookResult{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.green[command] {
		return agent.ToolCallHookResult{}
	}
	return agent.ToolCallHookResult{Block: true, Reason: "Not re-run: this exact test command already passed and no file has changed since. " +
		"If the work is done, finish; if you need a different check, run a different command."}
}

// after records a passing test command and forgets every pass once a
// file may have changed.
func (c *testCap) after(_ context.Context, _, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	command, isBash := bashCommand(toolName, args)
	switch {
	case isBash && testCommandRE.MatchString(command):
		if c.green == nil {
			c.green = map[string]bool{}
		}
		c.green[command] = !result.IsError
	case fileChangingTools[toolName] || (isBash && !readOnlyCommandRE.MatchString(command)):
		clear(c.green)
	}
	return agent.AfterToolCallResult{}
}

// initTestCap installs the cap when testRerunCap is on.
func (s *Session) initTestCap() {
	if s.efficiency == nil || !s.efficiency.cfg.TestRerunCap {
		return
	}
	c := &testCap{}
	s.agent.AddBeforeToolCallHook(c.before)
	s.agent.AddAfterToolCallHook(c.after)
}
