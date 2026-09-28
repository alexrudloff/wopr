package coding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/ai/aitest"
	"github.com/alexrudloff/wopr/internal/codingagent/queue"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
)

const childAnswer = "STATUS: done\nCONFIDENCE: high\nANSWER: Route is in demo.go.\nEVIDENCE:\n- demo.go:1 \"package demo\"\nNOT_CHECKED: none"

// childStream answers every child request with childAnswer once release is
// closed, or fails when the request is cancelled first.
type childStream struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func newChildStream() *childStream {
	return &childStream{release: make(chan struct{}), started: make(chan struct{})}
}

func (c *childStream) stream(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p := aitest.NewFauxProvider("fake", model.ID)
	p.SetResponses(aitest.Response{Content: []aitest.Block{aitest.Text(childAnswer)}})
	return p.Stream(ctx, transcript, options)
}

// newTaskSession is a persisted session whose task children answer through
// stream and whose finished background tasks land in delivered.
func newTaskSession(t *testing.T, services *Services, stream *childStream, opts SessionOptions) (*Session, chan agent.AgentMessage) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(services.CWD(), "demo.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Model = fakeModel()
	sess, err := NewSession(services, opts)
	if err != nil {
		t.Fatal(err)
	}
	sess.tasks.tool.StreamFn = stream.stream
	delivered := make(chan agent.AgentMessage, 4)
	sess.SetTaskDelivery(func(msg agent.AgentMessage) { delivered <- msg })
	// The session file is written from the first assistant message on.
	if _, err := sess.inner.AppendMessage(agent.AgentMessage{Assistant: &agent.AssistantMessage{
		Role: agent.RoleAssistant, Content: []ai.AssistantContentBlock{ai.TextContent{Text: "ok"}}, StopReason: "stop",
	}}); err != nil {
		t.Fatal(err)
	}
	return sess, delivered
}

func startBackgroundTask(t *testing.T, sess *Session, item string) string {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"description": "find Route", "type": "explore", "brief": "Where is Route?", "effort": "quick", "background": true, "item": item})
	res, err := sess.tasks.tool.Execute(context.Background(), "call-"+item, params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Details.(subagent.Details).ID
}

func queueItem(t *testing.T, sess *Session, id string) queue.Item {
	t.Helper()
	it, ok := sess.Queue().Get(id)
	if !ok {
		t.Fatalf("queue has no item %q: %+v", id, sess.Queue().Items())
	}
	return it
}

// Closing the session interrupts a running background task: it enters the
// queue as interrupted, the queue survives resume, and re-dispatching runs
// the same brief again against the same item.
func TestInterruptedTasksSurviveResumeAndRedispatch(t *testing.T) {
	services := newTestServices(t)
	first := newChildStream()
	sess, _ := newTaskSession(t, services, first, SessionOptions{})
	id := startBackgroundTask(t, sess, "")
	<-first.started
	path := sess.Path()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if it := queueItem(t, sess, id); it.Status != queue.Interrupted || len(it.Dispatch) == 0 {
		t.Fatalf("interrupted item %+v", it)
	}

	second := newChildStream()
	resumed, delivered := newTaskSession(t, services, second, SessionOptions{ResumePath: path})
	defer func() { _ = resumed.Close() }()
	pending := resumed.Queue().Interrupted()
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("resumed queue %+v", resumed.Queue().Items())
	}
	started, err := resumed.RedispatchInterrupted(context.Background())
	if err != nil || len(started) != 1 || started[0].Spec.Brief != "Where is Route?" || started[0].Spec.Item != id {
		t.Fatalf("redispatch %+v err %v", started, err)
	}
	if it := queueItem(t, resumed, id); it.Status != queue.InProgress || it.Task != started[0].ID {
		t.Fatalf("re-dispatched item %+v", it)
	}
	close(second.release)
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("the re-dispatched task never finished")
	}
	if it := queueItem(t, resumed, id); it.Status != queue.Completed {
		t.Fatalf("item after re-dispatch %+v", it)
	}
}

// update_plan items persist in the session and come back on resume.
func TestQueuePersistsAcrossResume(t *testing.T) {
	services := newTestServices(t)
	sess, _ := newTaskSession(t, services, newChildStream(), SessionOptions{})
	params, _ := json.Marshal(map[string]any{"steps": []map[string]string{{"id": "docs", "goal": "write the docs", "status": "blocked"}}})
	if _, err := sess.toolNamed("update_plan").Execute(context.Background(), "c1", params, nil); err != nil {
		t.Fatal(err)
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
	if it := queueItem(t, resumed, "docs"); it.Status != queue.Blocked || it.Goal != "write the docs" {
		t.Fatalf("resumed item %+v", it)
	}
}
