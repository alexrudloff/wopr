package codingagent

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// TestFooterContextAndStoredCost checks the footer's inputs: context usage is
// the last turn's total (not cumulative), and a resumed session reports the
// cost stored on its entries instead of re-pricing the tokens.
func TestFooterContextAndStoredCost(t *testing.T) {
	s := NewStatusLine(nil, nil)
	for _, tc := range []struct {
		usage ai.Usage
		want  int
	}{
		{ai.Usage{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}, 165},
		{ai.Usage{Input: 200, Output: 75, CacheRead: 20, CacheWrite: 15}, 310},
	} {
		s.SetTurnContextUsage(&tc.usage)
		if s.contextTokens != tc.want {
			t.Fatalf("contextTokens = %d, want %d (last turn only)", s.contextTokens, tc.want)
		}
	}

	path := filepath.Join(t.TempDir(), "session.jsonl")
	session := NewSession("footer-cost", t.TempDir())
	session.SetPath(path)
	for _, msg := range []agent.AgentMessage{
		{User: &agent.UserMessage{Role: agent.RoleUser}},
		{Assistant: &agent.AssistantMessage{
			Role: agent.RoleAssistant, Provider: "anthropic", ModelID: "claude-opus-4-8",
			Content:    []ai.AssistantContentBlock{ai.ToolCall{ID: "call", Name: "read"}},
			StopReason: ai.StopReasonToolUse,
			Usage:      &ai.Usage{Input: 1000, Output: 200, CacheRead: 3000, Cost: ai.UsageCost{Total: 0.123}},
		}},
		{ToolResult: &agent.ToolResultMessage{
			Role: agent.RoleToolResult, ToolCallID: "call", ToolName: "read",
			Usage: &ai.Usage{Input: 10, Output: 5, Cost: ai.UsageCost{Total: 0.05}},
		}},
	} {
		if _, err := session.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loadSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.FooterUsageTotals()
	if math.Abs(got.cost-0.173) > 1e-9 || got.input != 1010 || got.output != 205 || got.cacheRead != 3000 {
		t.Fatalf("resumed totals = %+v, want stored cost 0.173 and summed tokens", got)
	}
}
