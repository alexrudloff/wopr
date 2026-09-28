package pruning

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// fixture builds a real in-memory session so items, edits and projections
// go through the same code the Session uses.
type fixture struct {
	t        *testing.T
	sess     *codingagent.Session
	ordinals Ordinals
	seq      int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, sess: codingagent.NewSession("s", t.TempDir())}
}

func (f *fixture) append(message agent.AgentMessage) string {
	f.t.Helper()
	id, err := f.sess.AppendMessage(message)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) user(text string) string {
	return f.append(agent.AgentMessage{User: &agent.UserMessage{Role: agent.RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}})
}

func (f *fixture) text(text string) string {
	return f.append(agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: agent.RoleAssistant, Content: []ai.AssistantContentBlock{ai.TextContent{Text: text}}, StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli()}})
}

// call appends an assistant message with one tool call and its result.
func (f *fixture) call(name string, args ai.JsonObject, result string, isError bool) (callID string) {
	f.seq++
	callID = fmt.Sprintf("call_%d", f.seq)
	f.append(agent.AgentMessage{Assistant: &agent.AssistantMessage{
		Role:       agent.RoleAssistant,
		Content:    []ai.AssistantContentBlock{ai.ThinkingContent{Thinking: "thinking", ThinkingSignature: "sig"}, ai.ToolCall{ID: callID, Name: name, Arguments: args}},
		StopReason: ai.StopReasonToolUse, Timestamp: time.Now().UnixMilli(),
	}})
	f.append(agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Role: agent.RoleToolResult, ToolCallID: callID, ToolName: name, Content: []ai.ToolResultMessageContent{ai.TextContent{Text: result}}, IsError: isError, Timestamp: time.Now().UnixMilli()}})
	return callID
}

func (f *fixture) items() []Item {
	path := f.sess.Branch(*f.sess.LeafID())
	return BuildItems(f.sess.BuildSessionProjection(), path, &f.ordinals)
}

func (f *fixture) apply(edits []Edit) {
	f.t.Helper()
	for _, e := range edits {
		var replacement *codingagent.ContextEditReplacement
		if e.Replacement != nil {
			replacement = &codingagent.ContextEditReplacement{Content: e.Replacement}
		}
		if _, err := f.sess.AppendContextEdit(e.TargetID, replacement); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) messages() []agent.AgentMessage { return f.sess.BuildSessionProjection().Messages }

func big(label string) string { return strings.Repeat(label+" line of file content\n", 60) }

func resultText(items []Item, callID string) (string, bool) {
	for _, item := range items {
		if r := item.Message.ToolResult; r != nil && r.ToolCallID == callID {
			return r.Text(), true
		}
	}
	return "", false
}

func strategies(edits []Edit) map[string]int {
	out := map[string]int{}
	for _, e := range edits {
		out[e.Strategy]++
	}
	return out
}

// checkPairing fails when a tool call lacks its result before the next
// assistant or user message, or a result has no call.
func checkPairing(t *testing.T, messages []agent.AgentMessage) {
	t.Helper()
	open := map[string]bool{}
	for i, m := range messages {
		switch {
		case m.Assistant != nil || m.User != nil:
			if len(open) > 0 {
				t.Fatalf("message %d: tool calls %v have no result", i, open)
			}
			if m.Assistant != nil {
				for _, block := range m.Assistant.Content {
					if tc, ok := block.(ai.ToolCall); ok {
						open[tc.ID] = true
					}
				}
			}
		case m.ToolResult != nil:
			if !open[m.ToolResult.ToolCallID] {
				t.Fatalf("message %d: result %s has no call", i, m.ToolResult.ToolCallID)
			}
			delete(open, m.ToolResult.ToolCallID)
		}
	}
}

func TestDedupeKeepsTheLatestIdenticalRead(t *testing.T) {
	f := newFixture(t)
	f.user("look at a.go")
	first := f.call("read", ai.JsonObject{"path": "a.go"}, big("a"), false)
	f.text("ok")
	second := f.call("read", ai.JsonObject{"path": "./a.go"}, big("a"), false)
	edits := Plan(DefaultConfig(), f.items(), "/repo")
	if len(edits) != 1 || edits[0].Strategy != StrategyDedupe {
		t.Fatalf("edits = %+v, want one dedupe", edits)
	}
	f.apply(edits)
	items := f.items()
	if text, _ := resultText(items, first); !strings.HasPrefix(text, "[pruned: this call was repeated later") {
		t.Fatalf("older read = %q, want the dedupe stub", text)
	}
	if text, _ := resultText(items, second); text != big("a") {
		t.Fatal("the newest read must stay whole")
	}
	checkPairing(t, f.messages())
	if again := Plan(DefaultConfig(), items, "/repo"); len(again) != 0 {
		t.Fatalf("an edited entry must not be planned again: %+v", again)
	}
}

func TestDedupeDoesNotCrossAnInterveningEdit(t *testing.T) {
	f := newFixture(t)
	f.user("fix a.go")
	f.call("read", ai.JsonObject{"path": "a.go"}, big("old"), false)
	f.call("edit", ai.JsonObject{"path": "a.go", "edits": []any{map[string]any{"oldText": "x", "newText": "y"}}}, "Edited a.go", false)
	f.call("read", ai.JsonObject{"path": "a.go"}, big("new"), false)
	cfg := DefaultConfig()
	cfg.SupersedeReads = false
	if edits := Plan(cfg, f.items(), "/repo"); len(edits) != 0 {
		t.Fatalf("dedupe crossed an edit: %+v", edits)
	}
	// A failed edit changes nothing, so the reads are still identical.
	g := newFixture(t)
	g.user("fix a.go")
	g.call("read", ai.JsonObject{"path": "a.go"}, big("old"), false)
	g.call("edit", ai.JsonObject{"path": "a.go"}, "no match", true)
	g.call("read", ai.JsonObject{"path": "a.go"}, big("old"), false)
	if got := strategies(Plan(cfg, g.items(), "/repo")); got[StrategyDedupe] != 1 {
		t.Fatalf("strategies = %v, want a dedupe across a failed edit", got)
	}
}

func TestPurgeErrorsKeepsTheErrorAndTheCall(t *testing.T) {
	f := newFixture(t)
	f.user("fix it")
	huge := strings.Repeat("old text that did not match\n", 80)
	failed := f.call("edit", ai.JsonObject{"path": "a.go", "edits": []any{map[string]any{"oldText": huge, "newText": huge}}}, "Could not find the exact text in a.go", true)
	// Only one later assistant message: the model is still reacting.
	f.text("let me look again")
	if edits := Plan(DefaultConfig(), f.items(), "/repo"); len(edits) != 0 {
		t.Fatalf("purged before the model moved on: %+v", edits)
	}
	f.call("read", ai.JsonObject{"path": "a.go"}, "small", false)
	edits := Plan(DefaultConfig(), f.items(), "/repo")
	if got := strategies(edits); got[StrategyPurgeErrors] != 1 || len(edits) != 1 {
		t.Fatalf("edits = %+v, want one purge", edits)
	}
	f.apply(edits)
	items := f.items()
	if text, _ := resultText(items, failed); text != "Could not find the exact text in a.go" {
		t.Fatalf("error text = %q, want it kept", text)
	}
	var args ai.JsonObject
	var thinking bool
	for _, item := range items {
		if a := item.Message.Assistant; a != nil {
			for _, block := range a.Content {
				if tc, ok := block.(ai.ToolCall); ok && tc.ID == failed {
					args = tc.Arguments
				}
				if th, ok := block.(ai.ThinkingContent); ok && th.ThinkingSignature == "sig" {
					thinking = true
				}
			}
		}
	}
	if args["path"] != "a.go" {
		t.Fatalf("args = %v, want the small path kept", args)
	}
	raw, _ := json.Marshal(args)
	if strings.Contains(string(raw), "did not match") || !strings.Contains(string(raw), "pruned") {
		t.Fatalf("args = %s, want the large input dropped", raw)
	}
	if !thinking {
		t.Fatal("the purge must keep the message's other blocks")
	}
	checkPairing(t, f.messages())

	cfg := DefaultConfig()
	cfg.PurgeErrors = false
	if edits := Plan(cfg, newFixtureWithFailedEdit(t).items(), "/repo"); len(edits) != 0 {
		t.Fatalf("purgeErrors off still planned %+v", edits)
	}
}

func newFixtureWithFailedEdit(t *testing.T) *fixture {
	f := newFixture(t)
	f.user("fix it")
	huge := strings.Repeat("x", 4000)
	f.call("edit", ai.JsonObject{"path": "a.go", "edits": []any{map[string]any{"oldText": huge}}}, "no match", true)
	f.text("again")
	f.text("again")
	return f
}

func TestChooseBatchesAroundTheCache(t *testing.T) {
	items := make([]Item, 100)
	for i := range items {
		items[i].Tokens = 1000
	}
	early := Edit{Index: 5, Saved: 900, Strategy: StrategyDedupe}
	late := Edit{Index: 90, Saved: 900, Strategy: StrategyDedupe}
	warm := Policy{Horizon: 10, WriteReadRatio: 12.5}
	if got := Choose([]Edit{early, late}, nil, items, warm); len(got) != 0 {
		t.Fatalf("a warm cache broke for a small saving: %+v", got)
	}
	if got := Choose([]Edit{early, late}, nil, items, Policy{Cold: true}); len(got) != 2 {
		t.Fatalf("a cold cache must take every edit: %+v", got)
	}
	if got := Choose([]Edit{early, late}, nil, items, Policy{Force: true}); len(got) != 2 {
		t.Fatalf("force must take every edit: %+v", got)
	}
	compress := Edit{Index: 50, Strategy: StrategyCompress}
	got := Choose([]Edit{early, late}, []Edit{compress}, items, warm)
	if len(got) != 2 || got[0].Strategy != StrategyCompress || got[1].Index != 90 {
		t.Fatalf("got %+v, want the compress plus the free edit after it", got)
	}
	// A large saving near the end pays for its own rewrite.
	tail := []Edit{{Index: 95, Saved: 3000}, {Index: 97, Saved: 1000}}
	if got := Choose(tail, nil, items, warm); len(got) != 2 {
		t.Fatalf("got %+v, want the paying batch", got)
	}
}

func compressFixture(t *testing.T) (*fixture, []string) {
	f := newFixture(t)
	f.user("explore the repo")                                          // #1
	a := f.call("read", ai.JsonObject{"path": "a.go"}, big("a"), false) // #2
	b := f.call("grep", ai.JsonObject{"pattern": "TODO"}, big("b"), false)
	f.text("found them")
	f.user("now fix the TODO in a.go") // #4
	c := f.call("read", ai.JsonObject{"path": "a.go", "offset": 1}, big("c"), false)
	return f, []string{a, b, c}
}

func TestCompressReplacesASpanAndArchivesIt(t *testing.T) {
	f, calls := compressFixture(t)
	items := f.items()
	var archived string
	plan, err := PlanCompress(items, 2, 3, "a.go and grep show TODOs at a.go:10 and b.go:3", "", func(_, text string) string {
		archived = text
		return "obs_0123456789abcdef01234567"
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archived, "a line of file content") || !strings.Contains(archived, "call grep") {
		t.Fatalf("archive misses the original span: %q", archived)
	}
	f.apply(plan.Edits)
	after := f.items()
	messages := f.messages()
	checkPairing(t, messages)
	var hostText string
	for _, item := range after {
		if a := item.Message.Assistant; a != nil {
			for _, block := range a.Content {
				if text, ok := block.(ai.TextContent); ok && strings.HasPrefix(text.Text, "[compressed #2..#3") {
					hostText = text.Text
				}
			}
		}
	}
	if !strings.Contains(hostText, "obs_recall id obs_0123456789abcdef01234567") || !strings.Contains(hostText, "TODOs at a.go:10") {
		t.Fatalf("host = %q", hostText)
	}
	if text, _ := resultText(after, calls[0]); text != compressedStub {
		t.Fatalf("host call result = %q, want the stub", text)
	}
	if _, ok := resultText(after, calls[1]); ok {
		t.Fatal("the rest of the span must be omitted")
	}
	if text, _ := resultText(after, calls[2]); text != big("c") {
		t.Fatal("messages after the span must stay")
	}
	if plan.Saved <= 0 {
		t.Fatalf("saved = %d", plan.Saved)
	}
}

func TestMarkersAreStableAcrossRequests(t *testing.T) {
	f, _ := compressFixture(t)
	items := f.items()
	ordinals := make([]int, len(items))
	messages := make([]agent.AgentMessage, len(items))
	for i, item := range items {
		ordinals[i] = item.Ordinal
		messages[i] = item.Message
	}
	marked := Mark(messages, ordinals)
	if got := marked[0].User.Content[0].(ai.TextContent).Text; got != "[#1] explore the repo" {
		t.Fatalf("user marker = %q", got)
	}
	if messages[0].User.Content[0].(ai.TextContent).Text != "explore the repo" {
		t.Fatal("Mark mutated its input")
	}
	// A later compaction-free append keeps every earlier number.
	f.user("more")
	again := f.items()
	for i := range items {
		if again[i].Ordinal != items[i].Ordinal {
			t.Fatalf("ordinal %d changed from %d to %d", i, items[i].Ordinal, again[i].Ordinal)
		}
	}
}
