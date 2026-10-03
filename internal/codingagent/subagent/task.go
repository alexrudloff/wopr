package subagent

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/text"
)

// TypeExplore is the read-only investigation type.
const TypeExplore = "explore"

// TypeGeneral is a general-purpose child with the full tools: it reads,
// edits, writes, and runs commands in the session's own working directory,
// and its changes are the session's (/undo covers them).
const TypeGeneral = "general"

// TypePropose is a war council proposal: a read-only child answers the
// user's request its own way, for the orchestrator to synthesize. Only the
// council runs it; the task tool offers explore alone.
const TypePropose = "propose"

// TypeBuild is a war council candidate: a child that makes a code change in
// its own git worktree and runs the tests there, for the orchestrator to
// pick from or merge. Only the council runs it.
const TypeBuild = "build"

// LevelMechanical is the router's difficulty level for lookups.
const LevelMechanical = "mechanical"

// Request is one brief to route and run.
type Request struct {
	ID string
	// Spec is the brief as dispatched, with its type and effort defaulted.
	Spec
	// ContextTokens is the expected peak context of the child: the brief,
	// its anchor files, and the type's baseline. A child starts cold, so this
	// is what its first requests must prefill.
	ContextTokens int
	// Saturated reports providers at their concurrency limit.
	Saturated func(provider string) bool
}

// Route is where an attempt runs.
type Route struct {
	Model    *ai.Model
	Thinking ai.ThinkingLevel
	Provider string
	Spec     string
	Tier     string
	Reason   string
	// Source is how the route was chosen: "rule", "jev", "fallback", "off".
	Source string
	Domain string
	// Level is the brief's difficulty level ("mechanical" for lookups), or
	// "" when the router did not classify it.
	Level string
	// Mode is the routing mode the route was chosen under ("off" when
	// routing is off).
	Mode string
	// Handle is the router's decision, handed back to Escalate and Failover.
	Handle any
}

// Writer is what a general child works with: its tools and the session's
// file hooks for them, and the files it has changed so far.
type Writer struct {
	Tools  []agent.AgentTool
	Before []agent.BeforeToolCallHook
	After  []agent.AfterToolCallHook
	// Changed returns the files the child has changed, relative to the
	// working directory where they are inside it.
	Changed func() []string
}

// WriterHost is a Host that can run general children.
type WriterHost interface {
	// Writer returns a general child's tools and hooks for task id on
	// model.
	Writer(id string, model *ai.Model) Writer
}

// Host is what the task tool needs from the session.
type Host interface {
	Route(ctx context.Context, req Request) (Route, error)
	// Escalate returns a route on a more capable model, or false.
	Escalate(req Request, prev Route) (Route, bool)
	// Failover replaces a route whose provider failed, or false.
	Failover(prev Route, message string) (Route, bool)
	// Tools returns fresh read-only tools for an explore child.
	Tools() []agent.AgentTool
	// Archive stores text for obs_recall and returns its id, or "".
	Archive(key, text string) string
	// Project applies ObservationPack to a child's request context.
	Project(messages []agent.AgentMessage) []agent.AgentMessage
	// Log appends one attempt to the decision and outcome log.
	Log(Record)
	Cwd() string
}

// Record is one attempt in the decision and outcome log.
type Record struct {
	Time          string  `json:"time"`
	Session       string  `json:"session,omitempty"`
	Task          string  `json:"task"`
	Attempt       int     `json:"attempt"`
	Type          string  `json:"type"`
	Effort        string  `json:"effort"`
	BriefSHA      string  `json:"briefSha"`
	BriefTokens   int     `json:"briefTokens"`
	ContextTokens int     `json:"contextTokens"`
	Model         string  `json:"model"`
	Tier          string  `json:"tier,omitempty"`
	Source        string  `json:"source,omitempty"`
	Domain        string  `json:"domain,omitempty"`
	Reason        string  `json:"reason,omitempty"`
	Mode          string  `json:"mode,omitempty"`
	Status        string  `json:"status"`
	Confidence    string  `json:"confidence"`
	Verified      int     `json:"verified"`
	Quotes        int     `json:"quotes"`
	Turns         int     `json:"turns"`
	ToolCalls     int     `json:"toolCalls"`
	InputTokens   int     `json:"inputTokens"`
	OutputTokens  int     `json:"outputTokens"`
	CacheRead     int     `json:"cacheRead"`
	Cost          float64 `json:"cost"`
	DurationMs    int64   `json:"durationMs"`
	BudgetHit     string  `json:"budgetHit,omitempty"`
	// Outcome is "accepted", "escalated", "failed", or "failover".
	Outcome     string `json:"outcome"`
	Trigger     string `json:"trigger,omitempty"`
	EscalatedTo string `json:"escalatedTo,omitempty"`
}

// Details is the structured result the TUI renders as a task row.
type Details struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Running     bool    `json:"running,omitempty"`
	Current     string  `json:"current,omitempty"`
	Model       string  `json:"model"`
	Spec        string  `json:"spec"`
	Tier        string  `json:"tier,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	State       string  `json:"state,omitempty"`
	ToolCalls   int     `json:"toolCalls"`
	DurationMs  int64   `json:"durationMs"`
	Tokens      int     `json:"tokens"`
	Cost        float64 `json:"cost"`
	Verified    int     `json:"verified"`
	Quotes      int     `json:"quotes"`
	Escalated   string  `json:"escalated,omitempty"`
	Transcript  string  `json:"transcript,omitempty"`
	// Files are the files a general child changed.
	Files []string `json:"files,omitempty"`
	// Background marks a call that started a background task.
	Background bool `json:"background,omitempty"`
}

// Tool is the task tool.
type Tool struct {
	Host    Host
	Limiter *Limiter
	// SessionID identifies the parent session; children share one cache key
	// derived from it so sibling tasks reuse the same prompt prefix.
	SessionID string
	// StreamFn overrides child provider streams (tests).
	StreamFn agent.StreamFn
	// Registry tracks the session's tasks; nil leaves them untracked.
	Registry *Registry
	// CanBackground reports whether a finished background task can be
	// delivered to the orchestrator; without it background calls block.
	CanBackground func() bool
	// Finish runs after a successful run, before the task is marked done
	// (a build candidate's test run); report shows its phase live, and its
	// text is appended to the result.
	Finish func(ctx context.Context, report func(current string)) string

	// writers holds each running general child's Writer by task id.
	writers sync.Map
}

func (t *Tool) Name() string  { return "task" }
func (t *Tool) Label() string { return "Task" }

func (t *Tool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

// ConcurrencySafe: an explore task only reads; a general one may change
// files, so it waits for the calls before it.
func (t *Tool) ConcurrencySafe(args json.RawMessage) bool {
	var in struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(args, &in) == nil && (in.Type == "" || in.Type == TypeExplore)
}

// Schema is the tool definition. Its guidelines are the orchestrator's
// "when to delegate" rules, present only while the tool is active.
func (t *Tool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        "task",
		Description: "Delegate self-contained work to a subagent with fresh context, in this working directory. type explore: a read-only investigation (read, grep, find, ls, read-only bash) that returns a short answer whose quotes wopr verifies. type general: the full tools (read, edit, write, bash, web): it makes changes, runs builds and tests, and reports what it changed; its edits are this session's (/undo covers them). Several task calls in one message run in parallel.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{"type": "string", "description": "3-6 word label"},
				"type":        map[string]any{"type": "string", "enum": []string{TypeExplore, TypeGeneral}, "description": "explore: read-only investigation; general: can edit, write, and run commands"},
				"brief":       map[string]any{"type": "string", "description": "Everything the subagent knows: objective, known paths and facts, constraints, the exact answer wanted, when to stop"},
				"paths":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Files or directories to start from"},
				"effort":      map[string]any{"type": "string", "enum": []string{EffortQuick, EffortMedium, EffortThorough}, "description": "explore: quick is a lookup (where X is defined, callers of Y), medium understands one area, thorough surveys many files. general: the size of the change (a small fix, one feature, a large change)"},
				"background":  map[string]any{"type": "boolean", "description": "Run detached; the result arrives later as a message"},
				"item":        map[string]any{"type": "string", "description": "Queue item id this task works on"},
			},
			"required": []string{"description", "type", "brief"},
		},
		PromptGuidelines: []string{
			"Delegate with task: exploration across unknown or more than 3 files, open-ended search or research, and 2+ independent lookups (type explore, one task each, all in one message). Keep inline: a known file or symbol (read or grep it), a single command, a small edit, and anything that needs this conversation's judgment.",
			"Use type general for a self-contained change you can fully specify (implement X in these files, fix these failing tests, port this module), especially to run independent changes in parallel in the background while you keep working. Give each writer its own files: a file one running writer has changed is locked to it until it finishes, and other writers' (and your) edits to it are refused.",
			"Use background for longer or independent work and keep going; its result (with the files it changed) arrives as a message when it finishes. Keep quick lookups blocking. Review a writer's changes (git diff, the tests) before building on them.",
			"A task brief is all the subagent sees: give the objective, known paths and facts, constraints, the exact answer wanted, and when to stop. Treat its result as a lead: re-read cited lines before editing on its strength, and don't redo its work.",
		},
	}
}

type taskParams struct {
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Brief       string   `json:"brief"`
	Paths       []string `json:"paths"`
	Effort      string   `json:"effort"`
	Background  bool     `json:"background"`
	Item        string   `json:"item"`
}

const (
	maxAttempts  = 2
	maxFailovers = 3
	// resultBudgetBytes caps the result the orchestrator reads (~1.5K
	// tokens); the full result stays reachable through obs_recall.
	resultBudgetBytes = 6000
	answerBudgetBytes = 3000
)

// Execute routes the brief, runs the child, verifies its quotes, escalates
// once when the answer cannot be trusted, and returns the checked result. A
// background call returns once the task is routed and running; its result
// reaches the orchestrator through the registry's OnFinish.
func (t *Tool) Execute(ctx context.Context, toolCallID string, raw json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var p taskParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return agent.AgentToolResult{}, err
	}
	spec := Spec{Type: p.Type, Description: p.Description, Brief: p.Brief, Effort: p.Effort, Paths: p.Paths, Item: p.Item}
	id := NewTaskID(toolCallID + "\x00" + p.Brief)
	if p.Background && t.CanBackground != nil && t.CanBackground() && t.Registry != nil {
		a, err := t.Start(ctx, id, spec)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		label := a.Label()
		d := Details{ID: a.ID, Type: a.Spec.Type, Description: a.Spec.Description, Model: a.Model, Spec: a.ModelSpec, State: StateRunning, Background: true}
		return agent.AgentToolResult{
			Content: fmt.Sprintf("started %s (%s, %s). Its result arrives as a message when it finishes; keep working.", a.ID, a.Model, label),
			Details: d,
			Preview: "background · " + a.Model,
		}, nil
	}
	details, text, err := t.Run(ctx, id, spec, func(d Details) {
		if onUpdate != nil {
			onUpdate("↳ "+d.Current, d)
		}
	})
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	return agent.AgentToolResult{Content: text, Details: details, Preview: summaryLine(details)}, nil
}

// Run routes spec and runs it to its checked result on the caller's
// goroutine, tracked in the registry as a foreground task. progress, when
// set, receives live details. details.State is StatusDone only for an
// accepted, done result.
func (t *Tool) Run(ctx context.Context, id string, spec Spec, progress func(Details)) (Details, string, error) {
	req, route, err := t.prepare(ctx, id, spec)
	if err != nil {
		return Details{}, "", err
	}
	if t.Registry != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		t.Registry.add(id, req.Spec, false, route, cancel)
	}
	report := func(d Details) {
		if progress != nil {
			progress(d)
		}
		if t.Registry != nil {
			t.Registry.progress(id, d)
		}
	}
	details, text, err := t.execute(ctx, req, route, report)
	if err == nil && t.Finish != nil {
		text += t.Finish(ctx, func(current string) {
			d := details
			d.Running, d.Current = true, current
			report(d)
		})
	}
	if t.Registry != nil {
		if err != nil {
			text = err.Error()
			details.State = StatusFailed
		}
		t.Registry.finish(id, details, text)
	}
	return details, text, err
}

// Start routes spec and runs it in the background under id, detached from
// ctx's cancellation: only Registry.Stop and Registry.Interrupt end it. It
// returns once the task is routed and registered.
func (t *Tool) Start(ctx context.Context, id string, spec Spec) (Agent, error) {
	if t.Registry == nil {
		return Agent{}, errors.New("task: no registry for background tasks")
	}
	req, route, err := t.prepare(ctx, id, spec)
	if err != nil {
		return Agent{}, err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.Registry.wg.Add(1)
	t.Registry.add(id, req.Spec, true, route, cancel)
	go func() {
		defer t.Registry.wg.Done()
		defer cancel()
		details, text, err := t.execute(runCtx, req, route, func(d Details) { t.Registry.progress(id, d) })
		if err != nil {
			text = err.Error()
			details.State = StatusFailed
		}
		t.Registry.finish(id, details, text)
	}()
	a, _ := t.Registry.Get(id)
	return a, nil
}

// prepare validates spec and routes it.
func (t *Tool) prepare(ctx context.Context, id string, spec Spec) (Request, Route, error) {
	spec.Type = cmp.Or(spec.Type, TypeExplore)
	switch spec.Type {
	case TypeExplore, TypePropose, TypeBuild:
	case TypeGeneral:
		if _, ok := t.Host.(WriterHost); !ok {
			return Request{}, Route{}, errors.New("task type general is not available here (available: explore)")
		}
	default:
		return Request{}, Route{}, fmt.Errorf("unknown task type %q (available: explore, general)", spec.Type)
	}
	if strings.TrimSpace(spec.Brief) == "" {
		return Request{}, Route{}, errors.New("task needs a brief")
	}
	switch spec.Effort {
	case EffortQuick, EffortMedium, EffortThorough:
	default:
		spec.Effort = EffortMedium
	}
	req := Request{ID: id, Spec: spec, Saturated: t.Limiter.Saturated}
	req.ContextTokens = ExpectedContext(t.Host.Cwd(), req)
	route, err := t.Host.Route(ctx, req)
	if err != nil {
		return Request{}, Route{}, err
	}
	// A brief the router judged a lookup runs on the quick budget whatever
	// effort the orchestrator asked for.
	if route.Level == LevelMechanical && req.Effort != EffortQuick && req.Type == TypeExplore {
		req.Effort = EffortQuick
		req.ContextTokens = ExpectedContext(t.Host.Cwd(), req)
	}
	return req, route, nil
}

// execute runs a routed request to its checked result: the attempt loop
// with failover and one escalation. progress receives live details.
func (t *Tool) execute(ctx context.Context, req Request, route Route, progress func(Details)) (Details, string, error) {
	start := time.Now()
	details := Details{ID: req.ID, Type: req.Type, Description: req.Description}
	report := func(r Route, calls int, current string) {
		d := details
		d.Running, d.Current, d.ToolCalls = true, current, calls
		d.Model, d.Spec, d.Tier = modelName(r), r.Spec, r.Tier
		d.DurationMs = time.Since(start).Milliseconds()
		progress(d)
	}
	report(route, 0, "starting on "+modelName(route))

	var (
		res       Result
		out       Outcome
		err       error
		verified  int
		quotes    int
		trigger   string
		escalated string
		notes     string
		total     Outcome
		failovers int
	)
	for attempt := 1; attempt <= maxAttempts; {
		out, err = t.runAttempt(ctx, req, route, notes, func(calls int, current string) {
			report(route, total.ToolCalls+calls, current)
		})
		if err != nil {
			details.Model, details.Spec = modelName(route), route.Spec
			details.DurationMs = time.Since(start).Milliseconds()
			return details, "", err
		}
		accumulate(&total, out)
		if transcript := t.Host.Archive(fmt.Sprintf("%s:%d:%d", req.ID, attempt, failovers), out.Transcript); transcript != "" {
			details.Transcript = transcript
		}
		rec := t.record(req, attempt, route, out)
		if out.ProviderError != "" && out.ToolCalls == 0 && ctx.Err() == nil && failovers < maxFailovers {
			if next, ok := t.Host.Failover(route, out.ProviderError); ok {
				failovers++
				rec.Outcome, rec.Trigger, rec.EscalatedTo = "failover", out.ProviderError, next.Spec
				t.Host.Log(rec)
				route = next
				continue
			}
		}
		res = ParseResult(out.Text)
		verified, quotes = res.Verify(t.Host.Cwd(), out.Outputs)
		trigger = EscalationTrigger(res, verified, quotes, out)
		// A writer is judged by its changes and its checks, not its
		// quotes.
		if req.Type == TypeGeneral && strings.HasPrefix(trigger, "unverified quotes") {
			trigger = ""
		}
		if req.Type != TypeExplore && trigger == "no evidence" {
			// A proposal may be ideas or a plan, and a candidate is judged
			// by its diff and tests, with nothing to quote.
			trigger = ""
		}
		rec.Status, rec.Confidence, rec.Verified, rec.Quotes, rec.Trigger = res.Status, res.Confidence, verified, quotes, trigger
		rec.Outcome = "accepted"
		// A general child's changes are already in the working directory,
		// so it is never rerun on top of them.
		if trigger != "" && attempt < maxAttempts && ctx.Err() == nil && req.Type != TypeGeneral {
			if next, ok := t.Host.Escalate(req, route); ok {
				rec.Outcome, rec.EscalatedTo = "escalated", next.Spec
				t.Host.Log(rec)
				escalated = fmt.Sprintf("%s (%s)", route.Spec, trigger)
				notes = priorNotes(res, trigger)
				route = next
				attempt++
				continue
			}
		}
		if trigger != "" {
			rec.Outcome = "failed"
		}
		t.Host.Log(rec)
		break
	}
	state := res.Status
	if trigger != "" {
		state = StatusFailed
	}
	details.State = state
	details.Model, details.Spec, details.Tier, details.Reason = modelName(route), route.Spec, route.Tier, route.Reason
	details.ToolCalls = total.ToolCalls
	details.DurationMs = time.Since(start).Milliseconds()
	details.Tokens = total.Usage.Input + total.Usage.CacheRead + total.Usage.CacheWrite + total.Usage.Output
	details.Cost = total.Usage.Cost.Total
	details.Verified, details.Quotes = verified, quotes
	details.Escalated = escalated
	if w, ok := t.writers.LoadAndDelete(req.ID); ok && w.(Writer).Changed != nil {
		details.Files = w.(Writer).Changed()
	}
	return details, t.render(req, route, res, state, trigger, details, total), nil
}

func (t *Tool) runAttempt(ctx context.Context, req Request, route Route, notes string, progress func(int, string)) (Outcome, error) {
	release, err := t.Limiter.Acquire(ctx, route.Provider)
	if err != nil {
		return Outcome{}, err
	}
	defer release()
	tools := t.Host.Tools
	var before []agent.BeforeToolCallHook
	var after []agent.AfterToolCallHook
	if req.Type == TypeGeneral {
		w := t.Host.(WriterHost).Writer(req.ID, route.Model)
		t.writers.Store(req.ID, w)
		tools = func() []agent.AgentTool { return w.Tools }
		before, after = w.Before, w.After
	}
	return Run(ctx, Attempt{
		Model:     route.Model,
		Thinking:  route.Thinking,
		Tools:     tools(),
		Before:    before,
		After:     after,
		System:    systemPrompt(req.Type, t.Host.Cwd()),
		Prompt:    BriefPrompt(req, notes),
		Budget:    budgetFor(req),
		SessionID: t.SessionID + ":task",
		Project:   t.Host.Project,
		Progress:  progress,
		StreamFn:  t.StreamFn,
	}), nil
}

func (t *Tool) record(req Request, attempt int, route Route, out Outcome) Record {
	return Record{
		Time:          time.Now().UTC().Format(time.RFC3339),
		Session:       t.SessionID,
		Task:          req.ID,
		Attempt:       attempt,
		Type:          req.Type,
		Effort:        req.Effort,
		BriefSHA:      shortHash(req.Brief),
		BriefTokens:   ai.EstimateTextTokens(req.Brief),
		ContextTokens: req.ContextTokens,
		Model:         route.Spec,
		Tier:          route.Tier,
		Source:        route.Source,
		Domain:        route.Domain,
		Reason:        route.Reason,
		Mode:          route.Mode,
		Turns:         out.Turns,
		ToolCalls:     out.ToolCalls,
		InputTokens:   out.Usage.Input + out.Usage.CacheWrite,
		OutputTokens:  out.Usage.Output,
		CacheRead:     out.Usage.CacheRead,
		Cost:          out.Usage.Cost.Total,
		DurationMs:    out.Duration.Milliseconds(),
		BudgetHit:     out.BudgetHit,
	}
}

func accumulate(total *Outcome, out Outcome) {
	total.ToolCalls += out.ToolCalls
	total.Turns += out.Turns
	addUsage(&total.Usage, out.Usage)
}

// EscalationTrigger names why a result cannot be accepted as is, or "".
// wopr decides this from objective signals; the child is never asked
// whether it should escalate.
func EscalationTrigger(res Result, verified, quotes int, out Outcome) string {
	switch {
	case out.ProviderError != "":
		return "provider error: " + text.Clip(out.ProviderError, 80)
	case out.BudgetHit != "" && (!res.Parsed || res.Status != StatusDone):
		return "budget exhausted (" + out.BudgetHit + ")"
	case !res.Parsed:
		return "no result in the required format"
	case res.Status == StatusFailed || res.Status == StatusBlocked:
		return "status " + res.Status
	case res.Confidence == "low":
		return "low confidence"
	case !enoughVerified(verified, quotes):
		return fmt.Sprintf("unverified quotes %d/%d", quotes-verified, quotes)
	case quotes == 0 && res.Status == StatusDone:
		return "no evidence"
	}
	return ""
}

// enoughVerified accepts a result whose quotes mostly check out: at least
// two verified and at most a quarter unverified. The unverified ones are
// still marked for the orchestrator.
func enoughVerified(verified, quotes int) bool {
	return verified == quotes || (verified >= 2 && verified*4 >= quotes*3)
}

// priorNotes carries the first attempt's answer, not its transcript, into
// the escalated brief.
func priorNotes(res Result, trigger string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A previous attempt on a smaller model was not accepted (%s). Its notes are unverified leads, not facts:\n", trigger)
	if res.Answer != "" {
		b.WriteString("ANSWER: " + text.Clip(res.Answer, 1200) + "\n")
	}
	if res.NotChecked != "" {
		b.WriteString("NOT_CHECKED: " + text.Clip(res.NotChecked, 400) + "\n")
	}
	return b.String()
}

// render formats the checked result for the orchestrator, capped near
// resultBudgetBytes; the full text is archived when it does not fit.
func (t *Tool) render(req Request, route Route, res Result, state, trigger string, d Details, total Outcome) string {
	var body strings.Builder
	fmt.Fprintf(&body, "STATUS: %s · CONFIDENCE: %s\n", res.Status, res.Confidence)
	if trigger != "" {
		fmt.Fprintf(&body, "NOT ACCEPTED: %s. Treat the answer below as unverified.\n", trigger)
	}
	answer := cmp.Or(res.Answer, "(no answer)")
	full := answer
	answerBudget, resultBudget := answerBudgetBytes, resultBudgetBytes
	if req.Type != TypeExplore {
		answerBudget, resultBudget = 3*answerBudgetBytes, 2*resultBudgetBytes
	}
	answer = text.Clip(answer, answerBudget)
	body.WriteString("ANSWER: " + answer + "\n")
	if len(res.Evidence) > 0 {
		fmt.Fprintf(&body, "EVIDENCE (wopr verified %d/%d):\n", d.Verified, d.Quotes)
		for _, e := range res.Evidence {
			mark := "✓"
			if !e.Verified {
				mark = "✗"
			}
			line := mark + " " + text.Clip(e.Raw, 300)
			if e.Verified && e.Path != "" && e.Line > 0 && e.Line != e.Start {
				line += fmt.Sprintf(" (found at line %d)", e.Line)
			}
			if !e.Verified {
				line += " (" + e.Problem + ")"
			}
			body.WriteString(line + "\n")
		}
		if res.Dropped > 0 {
			fmt.Fprintf(&body, "(%d more evidence lines dropped)\n", res.Dropped)
		}
	}
	if res.NotChecked != "" {
		body.WriteString("NOT_CHECKED: " + text.Clip(res.NotChecked, 500) + "\n")
	}
	if req.Type == TypeGeneral {
		if len(d.Files) == 0 {
			body.WriteString("FILES CHANGED: none\n")
		} else {
			body.WriteString("FILES CHANGED (" + fmt.Sprint(len(d.Files)) + "): " + text.Clip(strings.Join(d.Files, ", "), 1500) + "\n")
		}
	}
	result := body.String()
	if len(full) > answerBudget || len(result) > resultBudget {
		if id := t.Host.Archive(req.ID+":result", "ANSWER: "+full+"\n"+result); id != "" {
			result = text.Clip(result, resultBudget) + fmt.Sprintf("\n(full result: obs_recall id=%s)\n", id)
		} else {
			result = text.Clip(result, resultBudget) + "\n"
		}
	}
	footer := []string{route.Spec}
	if route.Tier != "" || route.Source != "" {
		footer[0] += " (" + strings.Trim(route.Tier+", "+route.Source, ", ") + ")"
	}
	footer = append(footer,
		fmt.Sprintf("%d tool calls", total.ToolCalls),
		fmt.Sprintf("%d turns", total.Turns),
		formatDuration(time.Duration(d.DurationMs)*time.Millisecond),
		fmt.Sprintf("%s in, %s out", humanTokens(total.Usage.Input+total.Usage.CacheRead+total.Usage.CacheWrite), humanTokens(total.Usage.Output)),
		fmt.Sprintf("$%.4f", total.Usage.Cost.Total),
		fmt.Sprintf("verified %d/%d quotes", d.Verified, d.Quotes),
	)
	if d.Escalated != "" {
		footer = append(footer, "escalated from "+d.Escalated)
	}
	if d.Transcript != "" {
		footer = append(footer, "transcript: obs_recall id="+d.Transcript)
	}
	return fmt.Sprintf("<task id=%q type=%q state=%q>\n%s[wopr: %s]\n</task>", req.ID, req.Type, state, result, strings.Join(footer, " · "))
}

// summaryLine is the collapsed one-line view of a finished task.
func summaryLine(d Details) string {
	if d.Type == TypeGeneral {
		return fmt.Sprintf("%s · %d tool calls · %s · %d files changed", d.Model, d.ToolCalls, formatDuration(time.Duration(d.DurationMs)*time.Millisecond), len(d.Files))
	}
	return fmt.Sprintf("%s · %d tool calls · %s · verified %d/%d", d.Model, d.ToolCalls, formatDuration(time.Duration(d.DurationMs)*time.Millisecond), d.Verified, d.Quotes)
}

func modelName(r Route) string {
	if r.Model != nil && r.Model.DisplayName != "" {
		return r.Model.DisplayName
	}
	return r.Spec
}

// ExpectedContext estimates a child's peak context: the brief, the anchor
// files it was pointed at, and a per-effort baseline for what exploring
// reads.
func ExpectedContext(cwd string, req Request) int {
	tokens := ai.EstimateTextTokens(req.Brief) + 1500 // system prompt and tools
	if req.Type == TypeGeneral {
		tokens += 30000 // edits, builds, and test output
	}
	switch req.Effort {
	case EffortQuick:
		tokens += 6000
	case EffortThorough:
		tokens += 30000
	default:
		tokens += 15000
	}
	for _, p := range req.Paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		info, err := os.Stat(p)
		switch {
		case err != nil:
		case info.IsDir():
			tokens += 2000
		default:
			tokens += int(min(info.Size(), 200_000) / 4)
		}
	}
	return tokens
}

// NewTaskID derives a task id from seed.
func NewTaskID(seed string) string { return "task_" + shortHash(seed) }

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:10]
}

func humanTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}
