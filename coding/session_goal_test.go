package coding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai/aitest"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/goal"
)

func auditAnswer(verdict string) string {
	return "STATUS: done\nCONFIDENCE: high\nANSWER: " + verdict + "\nEVIDENCE:\n- demo.go:1 \"package demo\"\nNOT_CHECKED: none"
}

// newGoalSession is a persisted session whose orchestrator replies with
// replies in order and whose goal auditor answers with audits in order.
// briefs receives each audit brief.
func newGoalSession(t *testing.T, services *Services, opts SessionOptions, replies, audits []string) (*Session, *[]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(services.CWD(), "demo.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orchestrator := aitest.NewFauxProvider("fake", "fake-1")
	var steps []aitest.Response
	for _, reply := range replies {
		steps = append(steps, aitest.Response{Content: []aitest.Block{aitest.Text(reply)}})
	}
	orchestrator.SetResponses(steps...)
	opts.Model = fakeModelWithProvider(orchestrator)
	sess, err := NewSession(services, opts)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for ev := range sess.Events() {
			AcknowledgeEvent(ev)
		}
	}()
	var briefs []string
	var n atomic.Int32
	sess.tasks.tool.StreamFn = func(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
		if msgs := transcript.Messages(); len(msgs) > 0 {
			if user, ok := msgs[len(msgs)-1].(ai.UserMessage); ok {
				switch content := user.Content.(type) {
				case ai.UserText:
					briefs = append(briefs, string(content))
				case ai.UserContentBlocks:
					for _, block := range content {
						if text, ok := block.(ai.TextContent); ok {
							briefs = append(briefs, text.Text)
						}
					}
				}
			}
		}
		answer := audits[min(int(n.Add(1))-1, len(audits)-1)]
		p := aitest.NewFauxProvider("fake", model.ID)
		p.SetResponses(aitest.Response{Content: []aitest.Block{aitest.Text(answer)}})
		return p.Stream(ctx, transcript, options)
	}
	return sess, &briefs
}

// sendGoal runs one turn seeded with a goal message, as the frontends do.
func sendGoal(t *testing.T, sess *Session, msg agent.AgentMessage) {
	t.Helper()
	if _, err := sess.RunAgentPrompt(context.Background(), func(ctx context.Context) ([]agent.AgentMessage, error) {
		return sess.Agent().SendMessages(ctx, []agent.AgentMessage{msg})
	}); err != nil {
		t.Fatal(err)
	}
}

func goalContent(msg *agent.AgentMessage) string {
	if msg == nil {
		return ""
	}
	content, _ := msg.Custom["content"].(string)
	return content
}

// The loop continues after a turn that claims nothing, and ends when the
// independent audit passes the claim.
func TestGoalContinuesUntilTheAuditPasses(t *testing.T) {
	sess, briefs := newGoalSession(t, newTestServices(t), SessionOptions{NoSession: true},
		[]string{"Started on it.", "Done.\nGOAL MET\n- demo.go:1 \"package demo\""},
		[]string{auditAnswer("PASS: demo.go is the package")})
	defer func() { _ = sess.Close() }()
	msg, err := sess.SetGoal("create package demo", goal.DefaultCaps())
	if err != nil || !strings.Contains(goalContent(&msg), "create package demo") {
		t.Fatalf("kickoff %q %v", goalContent(&msg), err)
	}
	sendGoal(t, sess, msg)
	step := sess.GoalNext(context.Background())
	if step.Message == nil || !strings.Contains(goalContent(step.Message), "auto turn 1/20") {
		t.Fatalf("no continuation after an unfinished turn: %+v", step)
	}
	sendGoal(t, sess, *step.Message)
	step = sess.GoalNext(context.Background())
	if step.Event != "done" || step.Message != nil {
		t.Fatalf("a passed audit did not finish the goal: %+v", step)
	}
	if st, _ := sess.Goal(); st.Status != goal.Done || st.Audits != 1 {
		t.Fatalf("goal %+v", st)
	}
	if len(*briefs) != 1 || !strings.Contains((*briefs)[0], "create package demo") || !strings.Contains((*briefs)[0], `- demo.go:1 "package demo"`) {
		t.Fatalf("audit briefs %q", *briefs)
	}
	if step := sess.GoalNext(context.Background()); step.Message != nil || step.Event != "" {
		t.Fatalf("a finished goal continued: %+v", step)
	}
}

// The goal survives resume, paused so reopening never spends on its own;
// resume opens a fresh window and stop ends it.
func TestGoalPersistsAcrossResumeAndStopResume(t *testing.T) {
	services := newTestServices(t)
	sess, _ := newGoalSession(t, services, SessionOptions{}, []string{"working", "more"}, []string{auditAnswer("PASS")})
	msg, _ := sess.SetGoal("ship the demo", goal.Caps{Turns: 5, Cost: 1, Time: time.Hour})
	sendGoal(t, sess, msg)
	if step := sess.GoalNext(context.Background()); step.Message == nil {
		t.Fatalf("no continuation: %+v", step)
	}
	path := sess.Path()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := NewSession(services, SessionOptions{Model: fakeModel(), ResumePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	st, ok := resumed.Goal()
	if !ok || st.Objective != "ship the demo" || st.Status != goal.Paused || st.Reason != "session reopened" || st.Turns != 1 || st.Caps.Turns != 5 {
		t.Fatalf("resumed goal %+v %v", st, ok)
	}
	if step := resumed.GoalNext(context.Background()); step.Message != nil {
		t.Fatal("a paused goal continued")
	}
	next, err := resumed.ResumeGoal()
	if err != nil || !strings.Contains(goalContent(&next), "ship the demo") {
		t.Fatalf("resume %q %v", goalContent(&next), err)
	}
	if st, _ := resumed.Goal(); st.Status != goal.Active || st.Turns != 1 || st.Idle != 0 {
		t.Fatalf("resumed window %+v", st)
	}
	if !resumed.StopGoal() {
		t.Fatal("stop found no goal")
	}
	if step := resumed.GoalNext(context.Background()); step.Message != nil || step.Event != "" {
		t.Fatalf("a stopped goal continued: %+v", step)
	}
	if _, err := resumed.ResumeGoal(); err == nil {
		t.Fatal("a stopped goal resumed")
	}
}
