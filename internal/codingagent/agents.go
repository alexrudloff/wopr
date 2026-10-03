package codingagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/queue"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// Background subagents in the TUI: finished results reach the orchestrator
// as a follow-up of the active run or the seed of a new one, and render as
// a compact "task_<id> finished" block.

// agentsHost is what the TUI needs from a Session that runs subagents.
type agentsHost interface {
	Agents() *subagent.Registry
	SetTaskDelivery(func(agent.AgentMessage))
	OnAgentsChange(func())
	StopAgent(id string) bool
	AgentTranscript(id string) string
}

// agentsHost returns the session's subagent host, or nil.
func (m *InteractiveMode) agentsHost() agentsHost {
	host, _ := m.opts.SessionHandle.(agentsHost)
	return host
}

// startAgents installs the delivery of finished background tasks and
// redraws on task changes. The returned function uninstalls both.
func (m *InteractiveMode) startAgents(ctx context.Context) func() {
	host := m.agentsHost()
	if host == nil {
		return func() {}
	}
	host.SetTaskDelivery(func(msg agent.AgentMessage) { m.deliverTaskResult(ctx, msg) })
	if shells := m.backgroundShells(); shells != nil {
		shells.OnChange(func() { m.postUITask(func() { m.tuiInst.RequestRender() }) })
	}
	host.OnAgentsChange(func() {
		m.postUITask(func() {
			m.tuiInst.RequestRender()
			// A goal waiting on its background tasks goes on once none
			// runs; a delivered result has already started a run.
			if registry := host.Agents(); registry != nil && len(registry.Running()) == 0 {
				m.maybeContinueGoal()
			}
		})
	})
	return func() {
		host.SetTaskDelivery(nil)
		host.OnAgentsChange(nil)
		if shells := m.backgroundShells(); shells != nil {
			shells.OnChange(nil)
		}
	}
}

// taskDeliveryRetry is how long a result waits for a compaction to end.
const taskDeliveryRetry = time.Second

// deliverTaskResult hands a finished background task to the orchestrator on
// the owner loop.
func (m *InteractiveMode) deliverTaskResult(ctx context.Context, msg agent.AgentMessage) {
	_ = m.postToMain(ctx, func() { m.startOrQueueTaskResult(ctx, msg) })
}

// startOrQueueTaskResult queues msg as a follow-up of the active run, which
// reads it after its current turn, or starts a run with it when idle. A
// compaction in progress defers it.
func (m *InteractiveMode) startOrQueueTaskResult(ctx context.Context, msg agent.AgentMessage) {
	if m.agent == nil || ctx.Err() != nil {
		return
	}
	if d, ok := resultDetails(msg.Custom["details"]); ok && d.Spec != "" {
		provider, _, _ := strings.Cut(d.Spec, "/")
		m.recordTaskRun(m.targetName(provider))
	}
	if m.isCompacting {
		time.AfterFunc(taskDeliveryRetry, func() { m.deliverTaskResult(ctx, msg) })
		return
	}
	if m.enqueueIfTurnActive(func() { m.agent.FollowUp(msg) }) {
		return
	}
	m.runTurnWithImages(ctx, "", nil, func(runCtx context.Context) ([]agent.AgentMessage, error) {
		return m.agent.SendMessages(runCtx, []agent.AgentMessage{msg})
	})
}

// resultDetails decodes a task-result message's details: the live struct
// or the JSON a resumed transcript carries.
func resultDetails(v any) (subagent.ResultDetails, bool) {
	if d, ok := v.(subagent.ResultDetails); ok {
		return d, true
	}
	data, err := json.Marshal(v)
	if err != nil {
		return subagent.ResultDetails{}, false
	}
	var d subagent.ResultDetails
	if json.Unmarshal(data, &d) != nil || d.ID == "" {
		return subagent.ResultDetails{}, false
	}
	return d, true
}

// taskResultBlock is a delivered background result in the transcript: one
// "task_<id> finished" line, and the result the orchestrator read when
// expanded.
type taskResultBlock struct {
	tui.BaseComponent
	details  subagent.ResultDetails
	content  string
	hint     string
	expanded bool
}

// newTaskResultBlock renders a result; hint is the expand key.
func newTaskResultBlock(details subagent.ResultDetails, content, hint string) *taskResultBlock {
	return &taskResultBlock{details: details, content: content, hint: hint}
}

func (b *taskResultBlock) SetExpanded(expanded bool) {
	b.expanded = expanded
	b.Invalidate()
}

func (b *taskResultBlock) SetOutputPad(int) { b.Invalidate() }

func (b *taskResultBlock) Render(width int) []string {
	th := tui.ActiveTheme()
	d := b.details
	token := "success"
	if d.State != subagent.StateDone {
		token = "warning"
	}
	head := th.FgText(token, "◆") + " " + bold(th.FgText("text", d.ID+" finished")) + th.FgText("textMuted", " · "+d.Label)
	stats := []string{d.State, d.Model, fmt.Sprintf("%d tool calls", d.ToolCalls), formatDuration(time.Duration(d.DurationMs) * time.Millisecond)}
	if d.Tokens > 0 {
		stats = append(stats, formatTokens(d.Tokens)+" tokens")
	}
	if d.Cost > 0 {
		stats = append(stats, fmt.Sprintf("$%.4f", d.Cost))
	}
	if n := len(d.Files); n > 0 {
		stats = append(stats, fmt.Sprintf("changed %d: %s", n, strings.Join(d.Files, ", ")))
	}
	lines := []string{"", "   " + widthx.TruncateToWidth(head, max(1, width-3), "…", false)}
	sub := th.FgText("textMuted", "   ↳ "+strings.Join(stats, " · "))
	if !b.expanded && b.hint != "" {
		sub += th.FgText("textMuted", " · "+b.hint+" to expand")
	}
	lines = append(lines, widthx.TruncateToWidth(sub, width, "…", false))
	if b.expanded {
		for _, line := range widthx.WrapTextWithAnsi(b.content, max(1, width-5)) {
			lines = append(lines, th.FgText("textMuted", "   │ ")+line)
		}
	}
	return lines
}

// agentsRegistry returns the session's task registry, or nil.
func (m *InteractiveMode) agentsRegistry() *subagent.Registry {
	if host := m.agentsHost(); host != nil {
		return host.Agents()
	}
	return nil
}

// sessionQueue returns the session's work queue, or nil.
func (m *InteractiveMode) sessionQueue() *queue.Queue {
	if host, ok := m.opts.SessionHandle.(interface{ Queue() *queue.Queue }); ok {
		return host.Queue()
	}
	return nil
}

// agentsLive reports whether the sidebar's Agents section changes with the
// clock: an agent runs, or a finished one has yet to fold.
func (m *InteractiveMode) agentsLive(now time.Time) bool {
	if m.bgLive(now) {
		return true
	}
	registry := m.agentsRegistry()
	if registry == nil {
		return false
	}
	for _, a := range registry.List() {
		if a.Running() || now.Sub(a.Finished) <= sidebarAgentLinger+time.Second {
			return true
		}
	}
	return false
}

// runningAgentsText is the prompt's "2 running" indicator, or "".
func (m *InteractiveMode) runningAgentsText() string {
	n := 0
	if registry := m.agentsRegistry(); registry != nil {
		n = len(registry.Running())
	}
	for _, job := range m.backgroundShells().List() {
		if job.Running() {
			n++
		}
	}
	if n > 0 {
		return fmt.Sprintf("%d running", n)
	}
	return ""
}

// pickAgent lists this session's agents, newest first, and opens the chosen
// one's transcript; ctrl+k stops the highlighted one. With stopping set it
// lists only running agents and stops the chosen one.
func (m *InteractiveMode) pickAgent(stopping bool) {
	host := m.agentsHost()
	if host == nil || host.Agents() == nil {
		m.showFlash("Subagents are not available in this session")
		return
	}
	registry := host.Agents()
	load := func() []tui.DialogOption {
		agents := registry.List()
		slices.Reverse(agents)
		now := time.Now()
		var options []tui.DialogOption
		for _, a := range agents {
			if stopping && !a.Running() {
				continue
			}
			category := "Finished"
			if a.Running() {
				category = "Running"
			}
			options = append(options, tui.DialogOption{
				Title:       a.Label(),
				Description: a.ID + " · " + a.State,
				Category:    category,
				Footer:      a.Model + " · " + formatDuration(a.Elapsed(now).Truncate(time.Second)),
				Value:       a.ID,
			})
		}
		return options
	}
	options := load()
	if len(options) == 0 {
		if stopping {
			m.showFlash("No agents are running")
		} else {
			m.showFlash("No agents ran in this session")
		}
		return
	}
	title := "Agents"
	if stopping {
		title = "Stop agent"
	}
	dialog := tui.NewDialogSelect(title, options, "")
	if !stopping {
		dialog.Actions = []tui.DialogAction{{Title: "Stop", Key: "ctrl+k", Run: func(option tui.DialogOption) {
			host.StopAgent(option.Value)
			dialog.SetOptions(load())
		}}}
	}
	chosen, ok := m.runDialogSelect(dialog, dialogLarge)
	if !ok {
		return
	}
	if stopping {
		if host.StopAgent(chosen.Value) {
			m.showFlash("Stopped " + chosen.Value)
		}
		return
	}
	m.openAgentDialog(chosen.Value)
}

// agentsCommand runs /agents: no argument lists the agents, "stop [id]"
// stops one, and an id opens its transcript.
func (m *InteractiveMode) agentsCommand(args string) {
	fields := strings.Fields(args)
	switch {
	case len(fields) == 0:
		m.pickAgent(false)
	case fields[0] == "stop" && len(fields) == 1:
		m.pickAgent(true)
	case fields[0] == "stop":
		if host := m.agentsHost(); host != nil && host.StopAgent(fields[1]) {
			m.showFlash("Stopped " + fields[1])
		} else {
			m.showWarning("No running agent " + fields[1])
		}
	default:
		m.openAgentDialog(fields[0])
	}
}

// openAgentDialog shows an agent's brief, its tool calls or archived
// transcript, and its result, updating while it runs.
func (m *InteractiveMode) openAgentDialog(id string) {
	host := m.agentsHost()
	if host == nil || host.Agents() == nil {
		return
	}
	if _, ok := host.Agents().Get(id); !ok {
		m.showWarning("No agent " + id)
		return
	}
	// Close is focused first, so a stray Enter never stops the agent.
	view := &agentView{host: host, id: id, button: 1, height: func() int { return max(6, m.tuiInst.Height()/2) }}
	wake, stop := everySecond()
	defer stop()
	m.runDialog(modal{component: view, handleInput: view.HandleInput, done: func() bool { return view.done }, wake: wake}, dialogLarge)
}

// agentView is the agent transcript dialog: a header, then the brief,
// tool calls or transcript, and result, scrolled with the arrow keys.
type agentView struct {
	tui.BaseComponent
	host   agentsHost
	id     string
	height func() int
	top    int
	done   bool
	// button is the focused one of buttons().
	button int
	// transcript caches the archived transcript of a finished agent.
	transcript *string
}

// buttons are Stop and Close while the agent runs, then Close.
func (v *agentView) buttons() []string {
	if a, ok := v.host.Agents().Get(v.id); ok && a.Running() {
		return []string{"Stop", "Close"}
	}
	return []string{"Close"}
}

func (v *agentView) HandleInput(data string) {
	page := max(1, v.height()-2)
	buttons := v.buttons()
	v.button = min(v.button, len(buttons)-1)
	switch {
	case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"), tui.MatchesKeyID(data, "q"):
		v.done = true
	case tui.MatchesKeyID(data, "enter"):
		if buttons[v.button] == "Stop" {
			v.host.StopAgent(v.id)
			v.button = 0
		} else {
			v.done = true
		}
	case tui.MatchesKeyID(data, "tab"), tui.MatchesKeyID(data, "right"):
		v.button = (v.button + 1) % len(buttons)
	case tui.MatchesKeyID(data, "shift+tab"), tui.MatchesKeyID(data, "left"):
		v.button = (v.button + len(buttons) - 1) % len(buttons)
	case tui.MatchesKeyID(data, "up"), tui.MatchesKeyID(data, "k"):
		v.top--
	case tui.MatchesKeyID(data, "down"), tui.MatchesKeyID(data, "j"):
		v.top++
	case tui.MatchesKeyID(data, "pageUp"):
		v.top -= page
	case tui.MatchesKeyID(data, "pageDown"), tui.MatchesKeyID(data, "space"):
		v.top += page
	case tui.MatchesKeyID(data, "home"):
		v.top = 0
	case tui.MatchesKeyID(data, "end"):
		v.top = 1 << 30
	}
	v.Invalidate()
}

// body is the dialog's scrolled text for a.
func (v *agentView) body(a subagent.Agent) string {
	var b strings.Builder
	b.WriteString("Brief\n" + a.Spec.Brief + "\n")
	if !a.Running() && v.transcript == nil {
		text := v.host.AgentTranscript(a.ID)
		v.transcript = &text
	}
	if v.transcript != nil && *v.transcript != "" {
		b.WriteString("\nTranscript\n" + *v.transcript + "\n")
	} else if len(a.Log) > 0 {
		b.WriteString("\nTool calls\n")
		for _, line := range a.Log {
			b.WriteString("→ " + line + "\n")
		}
	}
	if a.Result != "" {
		b.WriteString("\nResult\n" + a.Result + "\n")
	}
	return b.String()
}

func (v *agentView) Render(width int) []string {
	th := tui.ActiveTheme()
	const padX = 4
	inner := max(1, width-2*padX)
	pad := strings.Repeat(" ", padX)
	a, ok := v.host.Agents().Get(v.id)
	if !ok {
		return []string{pad + th.FgText("textMuted", "No agent "+v.id)}
	}
	title := bold(th.FgText("text", widthx.TruncateToWidth(a.ID+" · "+a.Label(), max(1, inner-4), "…", false)))
	stats := []string{a.State, a.Model, formatDuration(a.Elapsed(time.Now()).Truncate(time.Second)), fmt.Sprintf("%d tool calls", a.ToolCalls)}
	if a.Tokens > 0 {
		stats = append(stats, formatTokens(a.Tokens)+" tokens")
	}
	if a.Cost > 0 {
		stats = append(stats, fmt.Sprintf("$%.4f", a.Cost))
	}
	out := []string{
		pad + spread(title, th.FgText("textMuted", "esc"), inner),
		pad + th.FgText("textMuted", widthx.TruncateToWidth(strings.Join(stats, " · "), inner, "…", false)),
		"",
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimRight(v.body(a), "\n"), "\n") {
		switch line {
		case "Brief", "Transcript", "Tool calls", "Result":
			lines = append(lines, bold(th.FgText("text", line)))
			continue
		}
		for _, wrapped := range widthx.WrapTextWithAnsi(line, inner) {
			lines = append(lines, th.FgText("textMuted", wrapped))
		}
	}
	height := v.height()
	v.top = max(0, min(v.top, len(lines)-height))
	for _, line := range lines[v.top:min(len(lines), v.top+height)] {
		out = append(out, pad+line)
	}
	buttons := v.buttons()
	v.button = min(v.button, len(buttons)-1)
	var row []string
	for i, b := range buttons {
		row = append(row, dialogButton(b, i == v.button))
	}
	hint := "↑↓ scroll · ←→ choose · enter select · esc close"
	if len(lines) > height {
		hint = fmt.Sprintf("%d-%d of %d · ", v.top+1, min(len(lines), v.top+height), len(lines)) + hint
	}
	return append(out, "", pad+strings.Join(row, "  "), pad+th.FgText("textMuted", widthx.TruncateToWidth(hint, inner, "…", false)), "")
}

// quitPrompt is the question asked before quitting while background agents
// run, such as "2 agents running (1 on Claude, $0.03 so far). Quit anyway?",
// or "" when none runs. target names an agent's provider; agents on the
// user's own hardware (free reports which) are not listed by provider.
func quitPrompt(agents []subagent.Agent, target func(provider string) string, free func(provider string) bool) string {
	count := 0
	cost := 0.0
	var paid []string
	byTarget := map[string]int{}
	for _, a := range agents {
		if !a.Running() || !a.Background {
			continue
		}
		count++
		cost += a.Cost
		provider, _, _ := strings.Cut(a.ModelSpec, "/")
		if provider == "" || free(provider) {
			continue
		}
		name := target(provider)
		if byTarget[name] == 0 {
			paid = append(paid, name)
		}
		byTarget[name]++
	}
	if count == 0 {
		return ""
	}
	var details []string
	for _, name := range paid {
		details = append(details, fmt.Sprintf("%d on %s", byTarget[name], name))
	}
	if cost >= 0.005 {
		details = append(details, fmt.Sprintf("$%.2f so far", cost))
	}
	text := fmt.Sprintf("%d %s running", count, plural(count, "agent", "agents"))
	if len(details) > 0 {
		text += " (" + strings.Join(details, ", ") + ")"
	}
	return text + ". Quit anyway?"
}

// requestQuit exits, first asking when background agents are running:
// they are cancelled with the process and recorded as interrupted.
func (m *InteractiveMode) requestQuit() {
	if registry := m.agentsRegistry(); registry != nil {
		if prompt := quitPrompt(registry.List(), m.targetName, m.freeProvider); prompt != "" {
			confirm := m.confirm
			if confirm == nil {
				confirm = m.confirmDialog
			}
			if !confirm(prompt) {
				return
			}
		}
	}
	m.requestShutdown()
}

// confirmDialog asks a yes/no question in a dialog; only "Quit" confirms.
func (m *InteractiveMode) confirmDialog(prompt string) bool {
	dialog := tui.NewDialogSelect(prompt, []tui.DialogOption{
		{Title: "Quit", Description: "Stop the agents and exit", Value: "yes"},
		{Title: "Keep working", Value: "no"},
	}, "yes")
	chosen, ok := m.runDialogSelect(dialog, dialogMedium)
	return ok && chosen.Value == "yes"
}

// redispatcher re-runs the briefs of interrupted queue items.
type redispatcher interface {
	RedispatchInterrupted(ctx context.Context) ([]subagent.Agent, error)
}

// interruptedAgents counts the queue items whose agent was interrupted.
func (m *InteractiveMode) interruptedAgents() int {
	if q := m.sessionQueue(); q != nil {
		return len(q.Interrupted())
	}
	return 0
}

// offerRedispatch tells the user, in a notice that stays in the transcript,
// about agents interrupted when the session last closed and how to run them
// again.
func (m *InteractiveMode) offerRedispatch() {
	if n := m.interruptedAgents(); n > 0 {
		text := fmt.Sprintf("%d %s interrupted when the session closed. Run %s again with %s → Re-dispatch interrupted agents.",
			n, plural(n, "agent was", "agents were"), plural(n, "it", "them"), m.keyHint(appCommandPalette))
		m.appendBorderedNotice(tui.NewPaddedText(tui.ActiveTheme().FgText("warning", text), statusIndent, 0, nil))
	}
}

// redispatchInterrupted runs the interrupted agents' briefs again.
func (m *InteractiveMode) redispatchInterrupted(ctx context.Context) {
	host, ok := m.opts.SessionHandle.(redispatcher)
	if !ok {
		return
	}
	started, err := host.RedispatchInterrupted(ctx)
	if err != nil {
		m.showToast("error", "Re-dispatch", err.Error())
	}
	if len(started) > 0 {
		m.showFlash(fmt.Sprintf("Re-dispatched %d %s", len(started), plural(len(started), "agent", "agents")))
	}
}
