package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/ai/aitest"
)

const sampleFile = "package demo\n\nfunc Route() string {\n\treturn \"cluster\"\n}\n"

func TestVerifyQuotesAreByteExact(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.go"), []byte(sampleFile), 0o600); err != nil {
		t.Fatal(err)
	}
	text := "STATUS: done\nCONFIDENCE: high\nANSWER: x\nEVIDENCE:\n" +
		"- demo.go:3 \"func Route() string {\"\n" + // exact
		"- demo.go:4 \"\treturn \\\"cluster\\\"\"\n" + // escaped quotes
		"- demo.go:3 \"func Route() int {\"\n" + // paraphrase
		"- demo.go:200 \"func Route() string {\"\n" + // far from the cited line
		"- missing.go:1 \"package demo\"\n" +
		"- cmd: rg Route \"demo.go:3:func Route\"\n" +
		"- cmd: rg Route \"invented output\"\n"
	r := ParseResult(text)
	ok, total := r.Verify(dir, []string{"demo.go:3:func Route() string {"})
	if ok != 3 || total != 7 {
		t.Fatalf("verified %d/%d: %+v", ok, total, r.Evidence)
	}
	want := []bool{true, true, false, false, false, true, false}
	for i, e := range r.Evidence {
		if e.Verified != want[i] {
			t.Errorf("evidence %d %q verified=%v want %v (%s)", i, e.Raw, e.Verified, want[i], e.Problem)
		}
	}
}

func TestCheckReadOnly(t *testing.T) {
	for _, cmd := range []string{
		"rg -n 'foo|bar' internal",
		"git log --oneline -5 | head -3",
		"find . -name '*.go' | wc -l",
		"sed -n '10,40p' main.go",
		"go doc ./agent Agent",
		`grep -rn "a;b" .`,
	} {
		if err := CheckReadOnly(cmd); err != nil {
			t.Errorf("%q should be allowed: %v", cmd, err)
		}
	}
	for _, cmd := range []string{
		"rm -rf /",
		"cat a > b",
		"ls; rm x",
		"ls && rm x",
		"echo $(rm x)",
		"cat `rm x`",
		`grep "$(rm x)" .`,
		"find . -delete",
		"find . -exec rm {} +",
		"rg --pre ./evil x",
		"git commit -m x",
		"git -c core.pager=sh log",
		"git diff --output=x",
		"go build ./...",
		"go env -w GOFLAGS=x",
		"sed -i s/a/b/ f",
		"sort -o out in",
		"true || rm x",
		"FOO=1 ls",
	} {
		if err := CheckReadOnly(cmd); err == nil {
			t.Errorf("%q should be rejected", cmd)
		}
	}
}

// fakeTool returns fixed output and counts calls.
type fakeTool struct {
	name, output string
	mu           sync.Mutex
	calls        int
}

func (f *fakeTool) Name() string { return f.name }

func (f *fakeTool) Label() string { return f.name }

func (f *fakeTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{Name: f.name, Parameters: map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}}}}
}

func (f *fakeTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

func (f *fakeTool) Execute(context.Context, string, json.RawMessage, agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return agent.AgentToolResult{Content: f.output}, nil
}

// scripted is a provider that answers each request with the next reply.
type scripted struct {
	mu      sync.Mutex
	replies []aitest.Response
	prompts []string
}

func (s *scripted) stream(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	s.mu.Lock()
	var texts []string
	for _, m := range transcript.Messages() {
		u, ok := m.(ai.UserMessage)
		if !ok {
			continue
		}
		switch c := u.Content.(type) {
		case ai.UserText:
			texts = append(texts, string(c))
		case ai.UserContentBlocks:
			for _, block := range c {
				if t, ok := block.(ai.TextContent); ok {
					texts = append(texts, t.Text)
				}
			}
		}
	}
	s.prompts = append(s.prompts, strings.Join(texts, "\n"))
	reply := aitest.Response{Content: []aitest.Block{aitest.Text("STATUS: failed\nCONFIDENCE: low\nANSWER: out of script")}}
	if len(s.replies) > 0 {
		reply, s.replies = s.replies[0], s.replies[1:]
	}
	s.mu.Unlock()
	p := aitest.NewFauxProvider(model.Provider.ID(), model.ID)
	p.SetResponses(reply)
	return p.Stream(ctx, transcript, options)
}

func toolCall(name string) aitest.Response {
	return aitest.Response{Content: []aitest.Block{aitest.ToolCall(name, map[string]any{"pattern": "Route"}, "")}, StopReason: "toolUse"}
}

func answer(text string) aitest.Response {
	return aitest.Response{Content: []aitest.Block{aitest.Text(text)}}
}

// fakeHost routes to "cheap" first and escalates to "strong".
type fakeHost struct {
	cwd       string
	grep      *fakeTool
	records   []Record
	escalated int
	mu        sync.Mutex
}

func model(name string) *ai.Model {
	return &ai.Model{ID: name, DisplayName: name, Provider: aitest.NewFauxProvider("fake", "")}
}

func (h *fakeHost) Route(context.Context, Request) (Route, error) {
	return Route{Model: model("cheap"), Provider: "fake", Spec: "fake/cheap", Tier: "free", Source: "rule"}, nil
}

func (h *fakeHost) Escalate(_ Request, prev Route) (Route, bool) {
	if prev.Spec == "fake/strong" {
		return Route{}, false
	}
	h.escalated++
	return Route{Model: model("strong"), Provider: "fake", Spec: "fake/strong", Tier: "subscription", Source: "rule"}, true
}

func (h *fakeHost) Failover(Route, string) (Route, bool) { return Route{}, false }

func (h *fakeHost) Tools() []agent.AgentTool { return []agent.AgentTool{h.grep} }

func (h *fakeHost) Archive(key, _ string) string { return "obs_" + strings.Repeat("a", 24) }

func (h *fakeHost) Project(m []agent.AgentMessage) []agent.AgentMessage {
	return m
}

func (h *fakeHost) Log(r Record) {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
}

func (h *fakeHost) Cwd() string { return h.cwd }

func newFakeHost(t *testing.T) *fakeHost {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.go"), []byte(sampleFile), 0o600); err != nil {
		t.Fatal(err)
	}
	return &fakeHost{cwd: dir, grep: &fakeTool{name: "grep", output: "demo.go:3:func Route() string {"}}
}

func runTask(t *testing.T, host *fakeHost, script *scripted) (string, Details) {
	t.Helper()
	tool := &Tool{Host: host, Limiter: NewLimiter(4, nil), SessionID: "s", StreamFn: script.stream}
	params, _ := json.Marshal(map[string]any{"description": "find Route", "type": "explore", "brief": "Where is Route defined?", "effort": "quick"})
	var updates int
	res, err := tool.Execute(context.Background(), "call1", params, func(string, any) { updates++ })
	if err != nil {
		t.Fatal(err)
	}
	if updates == 0 {
		t.Error("no progress updates")
	}
	return res.Content, res.Details.(Details)
}

const goodAnswer = "STATUS: done\nCONFIDENCE: high\nANSWER: Route is in demo.go.\nEVIDENCE:\n- demo.go:3 \"func Route() string {\"\n- cmd: grep Route \"demo.go:3:func Route\"\nNOT_CHECKED: none"

func TestTaskReturnsVerifiedResult(t *testing.T) {
	host := newFakeHost(t)
	script := &scripted{replies: []aitest.Response{toolCall("grep"), answer(goodAnswer)}}
	text, d := runTask(t, host, script)
	if host.escalated != 0 || d.State != StatusDone || d.Verified != 2 || d.Quotes != 2 || d.ToolCalls != 1 || d.Spec != "fake/cheap" {
		t.Fatalf("details %+v\n%s", d, text)
	}
	for _, want := range []string{`<task id="task_`, `state="done"`, "EVIDENCE (wopr verified 2/2)", "✓ demo.go:3", "[wopr: fake/cheap (free, rule) · 1 tool calls", "transcript: obs_recall id=obs_"} {
		if !strings.Contains(text, want) {
			t.Errorf("result lacks %q:\n%s", want, text)
		}
	}
	if len(host.records) != 1 || host.records[0].Outcome != "accepted" || host.records[0].BriefSHA == "" {
		t.Fatalf("log: %+v", host.records)
	}
	if !strings.Contains(script.prompts[0], "Where is Route defined?") || strings.Contains(script.prompts[0], "previous attempt") {
		t.Fatalf("brief prompt: %q", script.prompts[0])
	}
}

func TestTaskEscalatesUnverifiableQuotesOnce(t *testing.T) {
	host := newFakeHost(t)
	invented := "STATUS: done\nCONFIDENCE: high\nANSWER: Route is in router.go.\nEVIDENCE:\n- demo.go:3 \"func Route() int {\"\nNOT_CHECKED: none"
	script := &scripted{replies: []aitest.Response{answer(invented), toolCall("grep"), answer(goodAnswer)}}
	text, d := runTask(t, host, script)
	if host.escalated != 1 || d.Spec != "fake/strong" || d.State != StatusDone || !strings.Contains(d.Escalated, "fake/cheap (unverified quotes 1/1)") {
		t.Fatalf("details %+v\n%s", d, text)
	}
	if !strings.Contains(script.prompts[1], "previous attempt on a smaller model was not accepted (unverified quotes 1/1)") {
		t.Fatalf("escalated brief lacks prior notes: %q", script.prompts[1])
	}
	if len(host.records) != 2 || host.records[0].Outcome != "escalated" || host.records[0].EscalatedTo != "fake/strong" || host.records[1].Outcome != "accepted" {
		t.Fatalf("log: %+v", host.records)
	}
}

func TestTaskFailsAfterTheEscalatedAttemptFails(t *testing.T) {
	host := newFakeHost(t)
	low := "STATUS: partial\nCONFIDENCE: low\nANSWER: not sure\nEVIDENCE:\nNOT_CHECKED: everything"
	script := &scripted{replies: []aitest.Response{answer(low), answer(low)}}
	text, d := runTask(t, host, script)
	if host.escalated != 1 || d.State != StatusFailed || !strings.Contains(text, "NOT ACCEPTED: low confidence") {
		t.Fatalf("details %+v\n%s", d, text)
	}
	if last := host.records[len(host.records)-1]; last.Outcome != "failed" {
		t.Fatalf("log: %+v", host.records)
	}
}

func TestBudgetClosesToolsAndAsksForTheAnswer(t *testing.T) {
	grep := &fakeTool{name: "grep", output: "demo.go:3:func Route() string {"}
	script := &scripted{replies: []aitest.Response{toolCall("grep"), toolCall("grep"), toolCall("grep"), answer(goodAnswer)}}
	out := Run(context.Background(), Attempt{
		Model:    model("cheap"),
		Tools:    []agent.AgentTool{grep},
		Prompt:   "find Route",
		Budget:   Budget{Turns: 2, Time: time.Minute, Tokens: 1_000_000},
		StreamFn: script.stream,
	})
	if out.BudgetHit != "turns" || grep.calls != 2 || !strings.Contains(out.Text, "STATUS: done") {
		t.Fatalf("budget %q, grep ran %d times, text %q", out.BudgetHit, grep.calls, out.Text)
	}
	if !strings.Contains(out.Transcript, wrapUpNudge) || !strings.Contains(out.Transcript, budgetRefusal) {
		t.Fatalf("transcript lacks the wrap-up nudge or the refusal:\n%s", out.Transcript)
	}
	// A done answer after a budget hit is judged on its evidence; anything
	// less than done is a budget failure.
	res := ParseResult(out.Text)
	if trigger := EscalationTrigger(res, 1, 1, out); trigger != "" {
		t.Fatalf("done answer after budget: trigger %q", trigger)
	}
	res.Status = StatusPartial
	if trigger := EscalationTrigger(res, 1, 1, out); !strings.HasPrefix(trigger, "budget exhausted") {
		t.Fatalf("partial answer after budget: trigger %q", trigger)
	}
}
