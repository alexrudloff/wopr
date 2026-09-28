package coding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// fauxToolCallWithUsage answers with a tool call whose usage reports
// contextTokens of context.
func fauxToolCallWithUsage(name string, contextTokens int) scriptedResponse {
	return func([]ai.Message) *ai.AssistantMessage {
		return &ai.AssistantMessage{
			Content:  []ai.AssistantContentBlock{ai.ToolCall{ID: "call-" + name, Name: name, Arguments: ai.JsonObject{}}},
			Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonToolUse, Timestamp: time.Now().UnixMilli(),
			Usage: ai.Usage{Input: contextTokens, TotalTokens: contextTokens},
		}
	}
}

// The Session compacts before the next assistant response of a run once the
// projected context crosses the threshold, and
// re-projects the Session before every request.
func TestSessionCompactsBetweenTurnsOfOneRun(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{
		settings:      `{"compaction":{"enabled":true,"reserveTokens":2000,"keepRecentTokens":1}}`,
		contextWindow: 10_000, maxTokens: 1000,
		tools: []agent.AgentTool{&fakeTool{name: "noop"}},
	},
		fauxToolCallWithUsage("noop", 9_000),
		fauxReply("mid-run summary", ai.StopReasonStop, 0),
		fauxReply("done", ai.StopReasonStop, time.Second),
	)
	if _, err := h.session.Send(context.Background(), "long task"); err != nil {
		t.Fatal(err)
	}
	events := h.settle(t)
	if got := h.provider.callCount(); got != 3 {
		t.Fatalf("model calls = %d, want 3", got)
	}
	var threshold bool
	for _, event := range events {
		if start, ok := event.(agent.CompactionStartEvent); ok && start.Reason == "threshold" {
			threshold = true
		}
	}
	if !threshold {
		t.Fatal("no threshold compaction_start during the run")
	}
	h.provider.mu.Lock()
	second := h.provider.requests[2]
	h.provider.mu.Unlock()
	if !strings.Contains(second, "mid-run summary") {
		t.Fatalf("second request does not carry the compaction summary: %s", second)
	}
}
