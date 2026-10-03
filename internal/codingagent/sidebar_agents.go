package codingagent

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/goal"
	"github.com/alexrudloff/wopr/internal/codingagent/queue"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// sidebarAgentLinger is how long a finished agent keeps its own row before
// it folds into the "N done" line.
const sidebarAgentLinger = 60 * time.Second

// foldAgents orders the rows of the Agents section at now: running agents
// newest first, then finished ones newer than sidebarAgentLinger, newest
// first. Older finished agents fold into a count and their cost.
func foldAgents(agents []subagent.Agent, now time.Time) (rows []subagent.Agent, folded int, foldedCost float64) {
	var running, recent []subagent.Agent
	for _, a := range agents {
		switch {
		case a.Running():
			running = append(running, a)
		case now.Sub(a.Finished) < sidebarAgentLinger:
			recent = append(recent, a)
		default:
			folded++
			foldedCost += a.Cost
		}
	}
	slices.Reverse(running)
	slices.SortStableFunc(recent, func(a, b subagent.Agent) int { return b.Finished.Compare(a.Finished) })
	return append(running, recent...), folded, foldedCost
}

// agentSpinner animates a running agent's dot once a second.
var agentSpinner = []string{"◐", "◓", "◑", "◒"}

// agentGlyph is a finished agent's state mark and its theme token.
func agentGlyph(state string) (string, string) {
	switch state {
	case subagent.StateDone:
		return "✓", "success"
	case subagent.StateFailed:
		return "✗", "error"
	case subagent.StateInterrupted:
		return "!", "warning"
	}
	return "■", "textMuted"
}

// agentsSection lists this session's tasks: a running one as its label and
// elapsed time over its model, tool calls, tokens and cost; a finished one
// as one line for a minute, then folded into "N done · $x.xx". It is empty
// when no task ran. rows > 0 bounds its height.
func (s *sidebar) agentsSection(width, rows int, now time.Time) []string {
	side := &s.m.side
	side.agentRowIDs = side.agentRowIDs[:0]
	var agents []subagent.Agent
	if registry := s.m.agentsRegistry(); registry != nil {
		agents = registry.List()
	}
	jobs := visibleJobs(s.m.backgroundShells().List(), now)
	if len(agents) == 0 && len(jobs) == 0 {
		return nil
	}
	th := tui.ActiveTheme()
	visible, folded, foldedCost := foldAgents(agents, now)
	running := 0
	for _, a := range visible {
		if a.Running() {
			running++
		}
	}
	for _, job := range jobs {
		if job.Running() {
			running++
		}
	}
	title := bold(th.FgText("text", "Agents"))
	if running > 0 {
		title = spread(title, th.FgText("textMuted", fmt.Sprintf("%d running", running)), width)
	}
	out := []string{title}
	ids := []string{""}
	for _, a := range visible {
		label := strings.Join(strings.Fields(a.Label()), " ")
		if a.Running() {
			dot := hexFg(phosphorHex(), agentSpinner[int(now.Unix())%len(agentSpinner)])
			right := th.FgText("textMuted", formatDuration(a.Elapsed(now).Truncate(time.Second)))
			out = append(out, spread(dot+" "+th.FgText("text", widthx.TruncateToWidth(label, max(1, width-3-widthx.VisibleWidth(right)), "…", false)), right, width))
			stats := []string{fmt.Sprintf("%d calls", a.ToolCalls)}
			if a.Tokens > 0 {
				stats = append(stats, formatTokens(a.Tokens))
			}
			if a.Cost > 0 {
				stats = append(stats, fmt.Sprintf("$%.2f", a.Cost))
			}
			stats = append(stats, a.Model)
			out = append(out, th.FgText("textMuted", widthx.TruncateToWidth("  "+strings.Join(stats, " · "), width, "…", false)))
			ids = append(ids, a.ID, a.ID)
			continue
		}
		glyph, token := agentGlyph(a.State)
		right := a.State
		if a.Cost > 0 {
			right = fmt.Sprintf("$%.2f", a.Cost)
		}
		out = append(out, spread(th.FgText(token, glyph)+" "+th.FgText("textMuted", widthx.TruncateToWidth(label, max(1, width-3-widthx.VisibleWidth(right)), "…", false)), th.FgText("textMuted", right), width))
		ids = append(ids, a.ID)
	}
	for _, job := range jobs {
		command := "$ " + strings.Join(strings.Fields(job.Command), " ")
		if job.Running() {
			dot := hexFg(phosphorHex(), agentSpinner[int(now.Unix())%len(agentSpinner)])
			right := th.FgText("textMuted", formatDuration(job.Elapsed(now).Truncate(time.Second)))
			out = append(out, spread(dot+" "+th.FgText("text", widthx.TruncateToWidth(command, max(1, width-3-widthx.VisibleWidth(right)), "…", false)), right, width))
			out = append(out, th.FgText("textMuted", widthx.TruncateToWidth(fmt.Sprintf("  pid %d · background shell", job.PGID), width, "…", false)))
			ids = append(ids, job.ID, job.ID)
			continue
		}
		glyph, token := agentGlyph(job.State)
		if job.State == tools.ShellExited {
			glyph, token = agentGlyph(subagent.StateDone)
		}
		right := job.State
		if job.ExitCode != nil {
			right = fmt.Sprintf("exit %d", *job.ExitCode)
		}
		out = append(out, spread(th.FgText(token, glyph)+" "+th.FgText("textMuted", widthx.TruncateToWidth(command, max(1, width-3-widthx.VisibleWidth(right)), "…", false)), th.FgText("textMuted", right), width))
		ids = append(ids, job.ID)
	}
	if folded > 0 {
		out = append(out, th.FgText("textMuted", fmt.Sprintf("%d done · $%.2f", folded, foldedCost)))
		ids = append(ids, "")
	}
	if rows > 0 && len(out) > rows {
		hidden := len(out) - rows + 1
		out, ids = append(out[:rows-1], th.FgText("textMuted", fmt.Sprintf("+%d more", hidden))), append(ids[:rows-1], "")
	}
	side.agentRowIDs = ids
	return out
}

// spread puts right at the right edge of a width-column row after left.
func spread(left, right string, width int) string {
	gap := max(1, width-widthx.VisibleWidth(left)-widthx.VisibleWidth(right))
	return left + strings.Repeat(" ", gap) + right
}

// queueGlyph is an open queue item's mark and its theme token.
func queueGlyph(it queue.Item) (string, string) {
	switch it.Status {
	case queue.InProgress:
		if it.Task != "" {
			return "[▸]", "primary"
		}
		return "[•]", "warning"
	case queue.Blocked:
		return "[!]", "error"
	case queue.Failed:
		return "[✗]", "error"
	case queue.Interrupted:
		return "[↻]", "warning"
	}
	return "[ ]", "textMuted"
}

// queueSection lists the work queue's open items; completed ones fold into
// a count beside the title. It is empty when nothing is open. rows > 0
// bounds its height.
func (s *sidebar) queueSection(width, rows int) []string {
	q := s.m.sessionQueue()
	if q == nil {
		return nil
	}
	var open []queue.Item
	done := 0
	for _, it := range q.Items() {
		if it.Status.Open() {
			open = append(open, it)
		} else {
			done++
		}
	}
	var pinned *goal.State
	if host := s.m.goalHost(); host != nil {
		if st, ok := host.Goal(); ok && st.Open() {
			pinned = &st
		}
	}
	if len(open) == 0 && pinned == nil {
		return nil
	}
	th := tui.ActiveTheme()
	title := bold(th.FgText("text", "Queue"))
	if done > 0 {
		title = spread(title, th.FgText("textMuted", fmt.Sprintf("%d done", done)), width)
	}
	out := []string{title}
	if pinned != nil {
		out = append(out, goalRows(*pinned, width)...)
	}
	for _, it := range open {
		glyph, token := queueGlyph(it)
		goal := oneLine(it.Goal, max(1, width-4))
		text := th.FgText("textMuted", goal)
		if it.Status == queue.InProgress {
			text = th.FgText("text", goal)
		}
		out = append(out, th.FgText(token, glyph)+" "+text)
	}
	if rows > 0 && len(out) > rows {
		hidden := len(out) - rows + 1
		out = append(out[:rows-1], th.FgText("textMuted", fmt.Sprintf("+%d more", hidden)))
	}
	return out
}

// goalRows are the pinned goal: its objective, then its status, turns,
// spend, and time.
func goalRows(st goal.State, width int) []string {
	th := tui.ActiveTheme()
	token := "primary"
	if st.Status != goal.Active {
		token = "warning"
	}
	return []string{
		th.FgText(token, "◎") + " " + th.FgText("text", oneLine(st.Objective, max(1, width-2))),
		th.FgText("textMuted", widthx.TruncateToWidth("  "+st.Summary(), width, "…", false)),
	}
}

// visibleJobs are the background jobs the Agents section lists: running
// ones, newest first, then those that ended within sidebarAgentLinger.
func visibleJobs(jobs []tools.BackgroundShell, now time.Time) []tools.BackgroundShell {
	var running, recent []tools.BackgroundShell
	for _, job := range jobs {
		switch {
		case job.Running():
			running = append(running, job)
		case now.Sub(job.Finished) < sidebarAgentLinger:
			recent = append(recent, job)
		}
	}
	slices.Reverse(running)
	return append(running, recent...)
}
