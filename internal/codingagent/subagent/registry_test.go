package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/ai/aitest"
)

// gated holds each provider request until release is closed or the request
// is cancelled, then answers like scripted.
type gated struct {
	scripted
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func newGated(replies ...aitest.Response) *gated {
	return &gated{replies: replies, release: make(chan struct{}), started: make(chan struct{})}
}

func (g *gated) stream(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.scripted.stream(ctx, model, transcript, options)
}

func backgroundTool(host *fakeHost, stream *gated, finished chan<- Agent) *Tool {
	reg := NewRegistry()
	reg.OnFinish = func(a Agent) { finished <- a }
	return &Tool{Host: host, Limiter: NewLimiter(4, nil), SessionID: "s", StreamFn: stream.stream, Registry: reg, CanBackground: func() bool { return true }}
}

func startBackground(t *testing.T, tool *Tool, ctx context.Context) string {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"description": "find Route", "type": "explore", "brief": "Where is Route defined?", "effort": "quick", "background": true, "item": "q1"})
	res, err := tool.Execute(ctx, "call1", params, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(Details)
	if !d.Background || !strings.HasPrefix(res.Content, "started "+d.ID+" (cheap, find Route)") {
		t.Fatalf("background start result %q %+v", res.Content, d)
	}
	return d.ID
}

func waitFinished(t *testing.T, finished <-chan Agent) Agent {
	t.Helper()
	select {
	case a := <-finished:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("background task never finished")
	}
	return Agent{}
}

// A background call returns before the child answers; the verified result
// arrives through OnFinish, and cancelling the turn that started it (Esc)
// does not cancel it.
func TestBackgroundTaskOutlivesItsTurn(t *testing.T) {
	host := newFakeHost(t)
	stream := newGated(toolCall("grep"), answer(goodAnswer))
	finished := make(chan Agent, 1)
	tool := backgroundTool(host, stream, finished)
	turn, cancelTurn := context.WithCancel(context.Background())
	id := startBackground(t, tool, turn)
	if running := tool.Registry.Running(); len(running) != 1 || running[0].ID != id || running[0].Spec.Item != "q1" {
		t.Fatalf("running %+v", running)
	}
	cancelTurn()
	close(stream.release)
	a := waitFinished(t, finished)
	if a.State != StateDone || !strings.Contains(a.Result, "EVIDENCE (wopr verified 2/2)") || a.ToolCalls != 1 || a.Transcript == "" {
		t.Fatalf("finished %+v", a)
	}
	if len(tool.Registry.Running()) != 0 || len(a.Log) == 0 {
		t.Fatalf("registry after finish: %+v", tool.Registry.List())
	}
}

func TestStopCancelsABackgroundTask(t *testing.T) {
	host := newFakeHost(t)
	stream := newGated()
	finished := make(chan Agent, 1)
	tool := backgroundTool(host, stream, finished)
	id := startBackground(t, tool, context.Background())
	<-stream.started
	if !tool.Registry.Stop(id) {
		t.Fatal("Stop found no running task")
	}
	if a := waitFinished(t, finished); a.State != StateCancelled {
		t.Fatalf("stopped task ended %q", a.State)
	}
	if tool.Registry.Stop(id) {
		t.Fatal("Stop reported a finished task as running")
	}
}
