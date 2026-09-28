package codingagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
	"github.com/alexrudloff/wopr/internal/codingagent/goal"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// /goal in the TUI: after every settled run, while the editor is empty and
// nothing else waits, the session decides the next automatic turn off the
// owner loop, and the turn starts back on it.

// goalHost is what the TUI needs from a Session with a goal.
type goalHost interface {
	Goal() (goal.State, bool)
	SetGoal(objective string, caps goal.Caps) (agent.AgentMessage, error)
	StopGoal() bool
	ResumeGoal() (agent.AgentMessage, error)
	GoalNext(ctx context.Context) goal.Step
	OnGoalChange(fn func())
}

// askHost is a Session that can ask the user through the TUI.
type askHost interface {
	SetUserAsker(askuser.Asker)
}

func (m *InteractiveMode) goalHost() goalHost {
	host, _ := m.opts.SessionHandle.(goalHost)
	return host
}

// startGoalAndAsk redraws on goal changes and installs ask_user's asker.
// The returned function uninstalls both.
func (m *InteractiveMode) startGoalAndAsk() func() {
	if host, ok := m.opts.SessionHandle.(askHost); ok {
		host.SetUserAsker(m.askUser)
	}
	if host := m.goalHost(); host != nil {
		host.OnGoalChange(func() { m.postUITask(m.tuiInst.RequestRender) })
		if st, ok := host.Goal(); ok && st.Status == goal.Paused {
			m.appendChatBlock(tui.NewText(tui.ActiveTheme().FgText("textMuted", "Goal paused ("+st.Reason+"): "+oneLine(st.Objective, 80)+" · /goal resume to continue")))
		}
	}
	return func() {
		if host, ok := m.opts.SessionHandle.(askHost); ok {
			host.SetUserAsker(nil)
		}
		if host := m.goalHost(); host != nil {
			host.OnGoalChange(nil)
		}
	}
}

// goalCommand runs /goal: status with no argument, stop, resume, or a new
// objective with optional --turns N, --cost DOLLARS, --time DURATION caps.
func (m *InteractiveMode) goalCommand(args string) error {
	host := m.goalHost()
	if host == nil {
		return errors.New("goals are not available in this session")
	}
	switch strings.TrimSpace(args) {
	case "", "status":
		st, ok := host.Goal()
		if !ok {
			m.showFlash("No goal · /goal <objective> sets one")
			return nil
		}
		m.appendChatBlock(tui.NewText(bold("Goal") + " · " + st.Summary() + "\n" + st.Objective))
		return nil
	case "stop":
		if !host.StopGoal() {
			m.showFlash("No open goal")
			return nil
		}
		m.showFlash("Goal stopped")
		return nil
	case "resume":
		msg, err := host.ResumeGoal()
		if err != nil {
			return err
		}
		m.sendGoalMessage(msg)
		return nil
	}
	objective, caps, err := goal.ParseArgs(args)
	if err != nil {
		return err
	}
	msg, err := host.SetGoal(objective, caps)
	if err != nil {
		return err
	}
	m.sendGoalMessage(msg)
	return nil
}

// sendGoalMessage starts a turn with a goal message, or queues it as a
// follow-up of the active run.
func (m *InteractiveMode) sendGoalMessage(msg agent.AgentMessage) {
	if m.agent == nil {
		return
	}
	if m.enqueueIfTurnActive(func() { m.agent.FollowUp(msg) }) {
		return
	}
	m.runTurnWithImages(m.runCtx, "", nil, func(ctx context.Context) ([]agent.AgentMessage, error) {
		return m.agent.SendMessages(ctx, []agent.AgentMessage{msg})
	})
}

// goalCanContinue reports whether an automatic turn may start now: no run,
// no question or dialog, no compaction, and nothing typed. The user's input
// always goes first.
func (m *InteractiveMode) goalCanContinue() bool {
	return m.isIdle && !m.goalChecking && !m.isCompacting && m.pendingQuestion == nil && m.dialogDepth == 0 &&
		len(m.compactionQueue) == 0 && strings.TrimSpace(m.editor.Text()) == ""
}

// maybeContinueGoal asks the session, off the owner loop, what follows the
// settled run, and applies the answer back on it.
func (m *InteractiveMode) maybeContinueGoal() {
	host := m.goalHost()
	if host == nil || !m.goalCanContinue() {
		return
	}
	if st, ok := host.Goal(); !ok || st.Status != goal.Active {
		return
	}
	m.goalChecking = true
	ctx := m.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		step := host.GoalNext(ctx)
		if m.postToMain(ctx, func() { m.applyGoalStep(step) }) != nil {
			m.goalChecking = false
		}
	}()
}

// applyGoalStep reports a finished or paused goal and starts the next
// automatic turn unless the user got there first.
func (m *InteractiveMode) applyGoalStep(step goal.Step) {
	m.goalChecking = false
	switch step.Event {
	case "done":
		m.showToast("success", "Goal met", oneLine(step.Text, 200))
	case "paused":
		m.showToast("warning", "Goal paused", step.Text+" · /goal resume to continue")
	}
	if step.Message != nil && m.goalCanContinue() {
		m.sendGoalMessage(*step.Message)
	}
	m.tuiInst.RequestRender()
}

// goalBlock is a goal message in the transcript: one line, and the text
// the orchestrator read when expanded.
type goalBlock struct {
	tui.BaseComponent
	title, content string
	expanded       bool
}

func (b *goalBlock) SetExpanded(expanded bool) {
	b.expanded = expanded
	b.Invalidate()
}

func (b *goalBlock) SetOutputPad(int) { b.Invalidate() }

func (b *goalBlock) Render(width int) []string {
	th := tui.ActiveTheme()
	out := []string{"", th.FgText("primary", "◎") + " " + th.FgText("textMuted", oneLine(b.title, max(1, width-2)))}
	if b.expanded {
		for line := range strings.SplitSeq(b.content, "\n") {
			out = append(out, th.FgText("textMuted", "  "+line))
		}
	}
	return out
}

// newGoalBlock renders a goal message from its details.
func newGoalBlock(content string, details any) *goalBlock {
	d, _ := details.(map[string]any)
	title := "Goal set"
	if kind, _ := d["kind"].(string); kind == "continue" {
		title = "Goal · continuing"
		if turn, ok := d["turn"].(float64); ok {
			title += fmt.Sprintf(" (turn %d", int(turn))
			if turns, ok := d["turns"].(float64); ok && turns > 0 {
				title += fmt.Sprintf("/%d", int(turns))
			}
			title += ")"
		} else if turn, ok := d["turn"].(int); ok {
			title += fmt.Sprintf(" (turn %d", turn)
			if turns, ok := d["turns"].(int); ok && turns > 0 {
				title += fmt.Sprintf("/%d", turns)
			}
			title += ")"
		}
	}
	if objective, _ := d["objective"].(string); objective != "" {
		title += ": " + objective
	}
	return &goalBlock{title: title, content: content}
}

// oneLine collapses whitespace and truncates to n columns.
func oneLine(s string, n int) string {
	return widthx.TruncateToWidth(strings.Join(strings.Fields(s), " "), n, "…", false)
}
