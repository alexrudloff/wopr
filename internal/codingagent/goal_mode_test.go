package codingagent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/goal"
)

// goalHandle is a session handle with an active goal whose next step is
// step; next counts GoalNext calls.
type goalHandle struct {
	InteractiveSessionHandle
	next *atomic.Int32
	step goal.Step
}

func (h goalHandle) Goal() (goal.State, bool) {
	return goal.State{Status: goal.Active, Objective: "x"}, true
}
func (goalHandle) SetGoal(string, goal.Caps) (agent.AgentMessage, error) {
	return agent.AgentMessage{}, nil
}
func (goalHandle) StopGoal() bool                          { return false }
func (goalHandle) ResumeGoal() (agent.AgentMessage, error) { return agent.AgentMessage{}, nil }
func (goalHandle) OnGoalChange(func())                     {}
func (h goalHandle) GoalNext(context.Context) goal.Step {
	h.next.Add(1)
	return h.step
}

// The user's typing holds the automatic turn back; once the editor is empty
// the goal goes on.
func TestGoalContinuationWaitsForTheUser(t *testing.T) {
	m := newPendingDisplayHarness(t)
	var next atomic.Int32
	m.opts.SessionHandle = goalHandle{next: &next}
	m.editor.OnChange = m.onEditorChange
	ctx := t.Context()
	m.runCtx = ctx
	m.editor.SetText("wait, also")
	m.maybeContinueGoal()
	if next.Load() != 0 || m.goalChecking {
		t.Fatal("an automatic turn was decided while the user typed")
	}
	m.editor.SetText("")
	(<-m.uiTaskCh)()
	select {
	case apply := <-m.uiTaskCh:
		apply()
	case <-time.After(5 * time.Second):
		t.Fatal("the goal did not go on once the editor emptied")
	}
	if next.Load() != 1 || m.goalChecking {
		t.Fatalf("GoalNext calls %d, checking %v", next.Load(), m.goalChecking)
	}
	// A continuation decided just as the user starts typing is dropped.
	msg := agent.AgentMessage{Custom: map[string]any{"role": agent.RoleCustom, "customType": goal.MessageType, "content": "go on"}}
	m.editor.SetText("my turn")
	gen := m.runGen
	m.applyGoalStep(goal.Step{Message: &msg})
	if m.runGen != gen {
		t.Fatal("an automatic turn started over the user's typing")
	}
}
