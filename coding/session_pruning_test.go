package coding

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/pruning"
)

// fileReadTool returns a large, path-specific body for any path.
type fileReadTool struct{}

func (fileReadTool) Name() string                           { return "read" }
func (fileReadTool) Label() string                          { return "" }
func (fileReadTool) Schema() ai.ToolSchema                  { return ai.ToolSchema{Name: "read"} }
func (fileReadTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }
func (fileReadTool) Execute(_ context.Context, _ string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(params, &in)
	return agent.AgentToolResult{Content: fileBody(in.Path)}, nil
}

func fileBody(path string) string { return strings.Repeat("content of "+path+"\n", 100) }

func fauxCallWith(id, name string, args ai.JsonObject) scriptedResponse {
	return func([]ai.Message) *ai.AssistantMessage {
		return &ai.AssistantMessage{
			Content:  []ai.AssistantContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
			Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonToolUse, Timestamp: time.Now().UnixMilli(),
		}
	}
}

func (h *recoveryHarness) request(i int) string {
	h.provider.mu.Lock()
	defer h.provider.mu.Unlock()
	return h.provider.requests[i]
}

// idle moves the pruning cache clock back past the provider's cache TTL.
func (h *recoveryHarness) idle() {
	st := h.session.pruning
	st.mu.Lock()
	st.lastRequest = time.Now().Add(-2 * pruning.CacheTTL)
	st.mu.Unlock()
}

func (h *recoveryHarness) run(t *testing.T, prompt string) {
	t.Helper()
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	if _, err := h.session.Send(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	h.settle(t)
}

const pruningCompactionOff = `"compaction":{"enabled":false}`

// A duplicate read is pruned, but not while the provider cache is warm and
// the saving is too small to pay for rewriting it: the edit waits for the
// cache to go cold, then lands on the first request of the next prompt.
func TestPruningWaitsForAColdCache(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{
		settings: `{` + pruningCompactionOff + `}`,
		tools:    []agent.AgentTool{fileReadTool{}},
	},
		fauxCallWith("r1", "read", ai.JsonObject{"path": "a.go"}),
		fauxCallWith("r2", "read", ai.JsonObject{"path": "a.go"}),
		fauxReply("read twice", ai.StopReasonStop, 0),
		fauxReply("next", ai.StopReasonStop, 0),
	)
	h.run(t, "read a.go")
	if got := strings.Count(h.request(2), "content of a.go"); got != 200 {
		t.Fatalf("warm request carries %d body lines, want both reads whole (200)", got)
	}
	if edits := h.entries("context_edit"); len(edits) != 0 {
		t.Fatalf("pruned %d entries while the cache was warm", len(edits))
	}
	h.idle()
	h.run(t, "next")
	if got := strings.Count(h.request(3), "content of a.go"); got != 100 {
		t.Fatalf("cold request carries %d body lines, want one read (100)", got)
	}
	if !strings.Contains(h.request(3), "repeated later with the same arguments") {
		t.Fatal("the older read was not replaced by the dedupe stub")
	}
	var saved bool
	h.mu.Lock()
	for _, event := range h.events {
		if s, ok := event.(agent.SavingsEvent); ok && s.Mechanism == "Context pruning" && s.Tokens > 0 {
			saved = true
		}
	}
	h.mu.Unlock()
	if !saved {
		t.Fatal("no savings event for the sidebar")
	}
}

// The compress tool and its markers cost nothing below the threshold. Past
// it they are offered at the next cold moment, the model's compress call
// replaces the span from the next request on, and obs_recall restores it.
func TestCompressIsOfferedPastTheThresholdAndRestorable(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{
		settings:      `{` + pruningCompactionOff + `}`,
		contextWindow: 3000,
		tools:         []agent.AgentTool{fileReadTool{}},
	},
		fauxCallWith("r1", "read", ai.JsonObject{"path": "a.go"}),
		fauxCallWith("r2", "read", ai.JsonObject{"path": "b.go"}),
		fauxReply("read both", ai.StopReasonStop, 0),
		fauxCallWith("c1", "compress", ai.JsonObject{"from": 2, "to": 3, "summary": "a.go and b.go only hold filler"}),
		fauxReply("compressed", ai.StopReasonStop, 0),
	)
	h.run(t, "read a.go and b.go")
	for i := range 3 {
		if strings.Contains(h.request(i), `"name":"compress"`) || strings.Contains(h.request(i), "[#1]") {
			t.Fatalf("request %d offered compress before the cache went cold", i)
		}
	}
	h.idle()
	h.run(t, "tidy up")
	offer := h.request(3)
	if !strings.Contains(offer, `"name":"compress"`) || !strings.Contains(offer, "[#1] read a.go and b.go") || !strings.Contains(offer, "[#2] content of a.go") {
		t.Fatalf("the cold request after the threshold did not offer compress with markers: %s", offer)
	}
	after := h.request(4)
	if strings.Contains(after, "content of a.go") || strings.Contains(after, "content of b.go") {
		t.Fatal("the compressed span is still in context")
	}
	if !strings.Contains(after, "[compressed #2..#3") || !strings.Contains(after, "a.go and b.go only hold filler") {
		t.Fatalf("the summary is missing: %s", after)
	}
	start := strings.Index(after, "obs_recall id obs_")
	if start < 0 {
		t.Fatal("the summary does not name its archive")
	}
	id := after[start+len("obs_recall id ") : start+len("obs_recall id ")+28]
	recall := h.session.toolNamed("obs_recall")
	if recall == nil {
		t.Fatal("obs_recall was not offered with compress")
	}
	params, _ := json.Marshal(map[string]any{"id": id})
	result, err := recall.Execute(context.Background(), "x", params, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "content of a.go") || !strings.Contains(result.Content, "content of b.go") {
		t.Fatalf("recall did not restore the span: %s", result.Content)
	}
}
