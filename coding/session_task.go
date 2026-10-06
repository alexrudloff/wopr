package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Subagents: the task tool delegates a brief to a child agent routed per
// brief (see internal/codingagent/subagent and router/brief.go).

// TaskLogFileName is the append-only decision and outcome log of subagent
// attempts under the agent directory.
const TaskLogFileName = "task-log.jsonl"

// initSubagents creates the task tool, and installs it for the model when
// install is set; the goal's auditor runs through it either way. It runs
// after initEfficiency so children can use ObservationPack and obs_recall.
func (s *Session) initSubagents(install bool) {
	cfg := s.router.Config().Subagents
	registry := subagent.NewRegistry()
	tool := &subagent.Tool{
		Host:      taskHost{s: s},
		Limiter:   subagent.NewLimiter(cfg.MaxParallel, cfg.ProviderParallel),
		SessionID: s.ID(),
		Registry:  registry,
		// Without a frontend, finished background results wait for the
		// run (see awaitBackground).
		CanBackground: func() bool { return true },
	}
	s.tasks.tool = tool
	registry.OnStart = func(a subagent.Agent) {
		s.taskStarted(a)
		s.goalTaskStarted(a)
	}
	registry.OnFinish = func(a subagent.Agent) {
		s.tasks.locks.release(a.ID)
		s.goalTaskFinished(a)
		s.taskFinished(a)
	}
	registry.OnChange = func() {
		if fn := s.tasks.changed.Load(); fn != nil {
			(*fn)()
		}
	}
	if install {
		s.tools = append(s.tools, tool)
		s.agent.SetTools(append(s.agent.Tools(), tool))
		s.agent.AddBeforeToolCallHook(s.writerGuard)
		finish := s.agent.FinishTurnHook()
		s.agent.SetFinishTurn(func(ctx context.Context, turn agent.AgentTurnContext) *agent.AgentTurnDecision {
			s.awaitBackground(ctx, turn)
			if finish == nil {
				return nil
			}
			return finish(ctx, turn)
		})
	}
}

// sessionTasks is the Session's subagent state: the task tool with its
// registry, and the frontend hooks.
type sessionTasks struct {
	tool *subagent.Tool
	// deliver hands a finished background task's result to the frontend,
	// which queues it into the active run or starts a run with it. Without
	// it background calls run blocking.
	deliver atomic.Pointer[func(agent.AgentMessage)]
	// changed is called after any registry change.
	changed atomic.Pointer[func()]
	// files are the session's file hooks general children run; locks
	// holds the files running writers changed; headless queues background
	// results while no frontend delivers them.
	files    fileHooks
	locks    fileLocks
	headless headlessResults
}

// interruptWait bounds how long closing waits for background tasks to
// record their partial results.
const interruptWait = 3 * time.Second

// Agents returns the subagent task registry, or nil when the task tool is
// not installed.
func (s *Session) Agents() *subagent.Registry {
	if s.tasks.tool == nil {
		return nil
	}
	return s.tasks.tool.Registry
}

// SetTaskDelivery installs the frontend's delivery of finished background
// tasks. msg is a custom message the orchestrator reads as a user message:
// queued as a follow-up while a run is active, otherwise the seed of a new
// run. nil turns background tasks off.
func (s *Session) SetTaskDelivery(deliver func(msg agent.AgentMessage)) {
	if deliver == nil {
		s.tasks.deliver.Store(nil)
		return
	}
	s.tasks.deliver.Store(&deliver)
}

// OnAgentsChange sets the callback run after any task registry change, off
// the caller's goroutine.
func (s *Session) OnAgentsChange(fn func()) {
	if fn == nil {
		s.tasks.changed.Store(nil)
		return
	}
	s.tasks.changed.Store(&fn)
}

// StopAgent cancels a running task; it reports whether one was running.
func (s *Session) StopAgent(id string) bool {
	registry := s.Agents()
	return registry != nil && registry.Stop(id)
}

// AgentTranscript returns a finished task's archived transcript, or "".
func (s *Session) AgentTranscript(id string) string {
	registry := s.Agents()
	if registry == nil || s.efficiency == nil || s.efficiency.pack == nil {
		return ""
	}
	a, ok := registry.Get(id)
	if !ok || !efficiency.IsObservationID(a.Transcript) {
		return ""
	}
	data, err := os.ReadFile(efficiency.ObservationPath(s.efficiency.pack.Root(), a.Transcript))
	if err != nil {
		return ""
	}
	return string(data)
}

// taskFinished delivers a finished background task to the orchestrator. A
// task the user stopped is noted beside the next prompt instead, so
// stopping one never starts a run.
func (s *Session) taskFinished(a subagent.Agent) {
	note := s.queueTaskFinished(a)
	if !a.Background {
		return
	}
	switch a.State {
	case subagent.StateInterrupted:
		return
	case subagent.StateCancelled:
		s.agent.QueueNextTurn(agent.AgentMessage{Custom: map[string]any{
			"role": agent.RoleCustom, "customType": subagent.ResultMessageType, "display": false,
			"content":   fmt.Sprintf("Background %s (%s) was stopped by the user.", a.ID, a.Label()),
			"details":   a.ResultDetails(),
			"timestamp": time.Now().UnixMilli(),
		}})
		return
	}
	deliver := s.tasks.deliver.Load()
	if deliver == nil {
		s.tasks.headless.push(taskResultMessage(a, note))
		return
	}
	(*deliver)(taskResultMessage(a, note))
}

// taskResultMessage is a finished background task as the orchestrator
// reads it; note reports what changed in the work queue.
func taskResultMessage(a subagent.Agent, note string) agent.AgentMessage {
	head := fmt.Sprintf("Background %s (%s) finished: %s.", a.ID, a.Label(), a.State)
	if note != "" {
		head += " " + note
	}
	return agent.AgentMessage{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": subagent.ResultMessageType,
		"content":    head + "\n" + a.Result,
		"display":    true,
		"details":    a.ResultDetails(),
		"timestamp":  time.Now().UnixMilli(),
	}}
}

// interruptTasks cancels the running background tasks, which end as
// interrupted with their partial transcripts archived.
func (s *Session) interruptTasks() {
	if registry := s.Agents(); registry != nil {
		registry.Interrupt(interruptWait)
	}
}

// toolAllowed reports whether the session options let a harness tool in.
func toolAllowed(opts SessionOptions, name string) bool {
	has := func(set map[string]struct{}) bool { _, ok := set[name]; return ok }
	return !opts.SkipBuiltinTools &&
		(opts.ActiveBuiltinTools == nil || has(opts.ActiveBuiltinTools)) &&
		(opts.AllowedTools == nil || has(opts.AllowedTools)) &&
		!has(opts.ExcludedTools)
}

// taskHost adapts the Session to subagent.Host.
type taskHost struct{ s *Session }

var taskLogMu sync.Mutex

func (h taskHost) Cwd() string { return h.s.services.CWD() }

// Persona is the saved system prompt the session runs with, for subagents.
func (h taskHost) Persona() string {
	if p := h.s.persona.Load(); p != nil {
		return *p
	}
	return ""
}

// Route picks the child's model: the router's brief decision while
// subagents are routed (an error with the reason when no model is
// eligible), else the model chosen for subagents or the orchestrator's,
// and only when the routing mode admits it (see objectiveGate).
func (h taskHost) Route(ctx context.Context, req subagent.Request) (subagent.Route, error) {
	s := h.s
	if s.subagentRoutingActive() {
		in := h.briefInput(req)
		d := s.router.DecideBrief(ctx, in)
		s.notePause()
		if d == nil {
			return subagent.Route{}, fmt.Errorf("subagents: %w", s.noRouteError(in.ContextTokens, s.router.SubagentObjective()))
		}
		route, err := h.routeFor(d)
		if err != nil {
			return subagent.Route{}, fmt.Errorf("subagents: routed to %s, which is unavailable: %w", d.Spec(), err)
		}
		return route, nil
	}
	model := s.subagentBaseModel()
	if model == nil {
		return subagent.Route{}, errors.New("task: no model is selected")
	}
	if err := s.subagentGate(model); err != nil {
		return subagent.Route{}, err
	}
	source, reason := "off", "runs on the orchestrator's model"
	if s.subagentModel != "" {
		source, reason = "chosen", "runs on the model chosen for subagents"
	}
	thinking := s.agent.ThinkingLevel()
	if rs := s.route.Load(); rs != nil && s.routingActive() && rs.model == model {
		thinking = rs.thinking
	}
	return subagent.Route{
		Model:    model,
		Thinking: ai.ClampThinkingLevel(model, thinking),
		Provider: providerID(model),
		Spec:     providerID(model) + "/" + model.ID,
		Reason:   reason,
		Source:   source,
		Mode:     s.routingMode(),
	}, nil
}

func (h taskHost) briefInput(req subagent.Request) router.BriefInput {
	orchestrator := ""
	if m := h.s.activeModel(); m != nil {
		orchestrator = providerID(m) + "/" + m.ID
	}
	return router.BriefInput{
		Type:          req.Type,
		Effort:        req.Effort,
		Brief:         req.Brief,
		ContextTokens: req.ContextTokens,
		Orchestrator:  orchestrator,
		Saturated:     req.Saturated,
		Level:         req.Level,
		AvoidFamily:   req.AvoidFamily,
	}
}

func (h taskHost) routeFor(d *router.Decision) (subagent.Route, error) {
	m, err := h.s.routeModel(d.Provider, d.Model)
	if err != nil {
		return subagent.Route{}, err
	}
	route := subagent.Route{
		Model:    m,
		Thinking: h.s.routeThinking(m, d),
		Provider: d.Provider,
		Spec:     d.Spec(),
		Tier:     d.Tier,
		Reason:   d.Reason,
		Mode:     string(d.Objective),
		Handle:   d,
	}
	if d.Brief != nil {
		route.Source, route.Domain, route.Level = d.Brief.Source, d.Brief.Domain, d.Brief.Level
	}
	return route, nil
}

// Escalate moves an unaccepted brief one step up in capability.
func (h taskHost) Escalate(req subagent.Request, prev subagent.Route) (subagent.Route, bool) {
	d, ok := prev.Handle.(*router.Decision)
	if !ok || !h.s.subagentRoutingActive() {
		return subagent.Route{}, false
	}
	for next := h.s.router.EscalateBrief(d, h.briefInput(req)); next != nil; next = h.s.router.EscalateBrief(next, h.briefInput(req)) {
		if route, err := h.routeFor(next); err == nil {
			return route, true
		}
	}
	return subagent.Route{}, false
}

// Failover replaces a route whose provider failed, as the orchestrator's
// failover does; it does not count as an escalation.
func (h taskHost) Failover(prev subagent.Route, message string) (subagent.Route, bool) {
	d, ok := prev.Handle.(*router.Decision)
	if !ok {
		return subagent.Route{}, false
	}
	failure := router.Failure{Message: message, RateLimited: rateLimitPattern.MatchString(message), ContextTokens: d.ContextTokens}
	for next := h.s.router.Next(d, failure); next != nil; next = h.s.router.Next(next, failure) {
		next.Brief = d.Brief
		if route, err := h.routeFor(next); err == nil {
			return route, true
		}
	}
	return subagent.Route{}, false
}

// Tools returns fresh read-only tools: read, grep, find, ls, bash limited
// to read-only pipelines, and obs_recall when ObservationPack is on. A
// child never gets task (no nesting), edit, write, or update_plan.
func (h taskHost) Tools() []agent.AgentTool {
	s := h.s
	var out []agent.AgentTool
	for _, tool := range tools.CreateCodingTools(s.services.CWD(), s.services.Settings(), filepath.Join(s.services.AgentDir(), "bin")) {
		switch t := tool.(type) {
		case *tools.BashTool:
			t.HideSessionEnvironment = true
			out = append(out, subagent.ReadOnlyShell(t))
		default:
			switch tool.Name() {
			case "read", "grep", "find", "ls":
				out = append(out, tool)
			}
		}
	}
	if s.efficiency != nil && s.efficiency.pack != nil {
		out = append(out, efficiency.NewRecallTool(s.efficiency.pack))
	}
	return out
}

func (h taskHost) Archive(key, text string) string {
	s := h.s
	if s.efficiency == nil || s.efficiency.pack == nil || text == "" {
		return ""
	}
	id, err := s.efficiency.pack.Archive("task", key, text)
	if err != nil {
		return ""
	}
	return id
}

func (h taskHost) Project(messages []agent.AgentMessage) []agent.AgentMessage {
	return h.s.efficiencyProject(messages)
}

// Log appends one attempt to <agentDir>/task-log.jsonl. The log never
// leaves the machine.
func (h taskHost) Log(rec subagent.Record) {
	// Each attempt is also an outcome signal for its model.
	if rec.Outcome != "failover" && rec.Model != "" {
		o := router.OutcomeCounts{Tasks: 1, Quotes: rec.Quotes, QuotesUnverified: max(0, rec.Quotes-rec.Verified)}
		switch rec.Outcome {
		case "escalated":
			o.Escalated = 1
		case "failed":
			o.Failed = 1
		}
		router.RecordOutcome(h.s.services.AgentDir(), rec.Model, o)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	taskLogMu.Lock()
	defer taskLogMu.Unlock()
	f, err := os.OpenFile(filepath.Join(h.s.services.AgentDir(), TaskLogFileName), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}
