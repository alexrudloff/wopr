package codingagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// newStreamingRoutingMode starts a turn that streams until the returned
// release function is called, with the owner loop draining posted tasks.
func newStreamingRoutingMode(t *testing.T) (*InteractiveMode, context.Context, func()) {
	t.Helper()
	m := newPendingDisplayHarness(t)
	provider := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	model := &ai.Model{ID: "m", Provider: provider, Capabilities: ai.ModelCapabilities{ContextWindow: 800000}}
	m.opts.Model = model
	m.agent = agent.NewAgent(agent.AgentOptions{Model: model})
	m.chatContainer = tui.NewContainer()
	m.statusContainer = tui.NewContainer()
	m.slashRegistry = NewSlashRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	m.runCtx = ctx
	m.abortCtx, m.abortFn = context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go m.drainLoop(ctx, loopDone)
	var once sync.Once
	release := func() { once.Do(func() { close(provider.release) }) }
	t.Cleanup(func() {
		release()
		deadline := time.Now().Add(5 * time.Second)
		for m.turnActive.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		<-loopDone
	})
	m.handleSubmit(ctx, "first prompt")
	<-provider.started
	return m, ctx, release
}

// onLoop runs fn on the owner loop and waits for it.
func onLoop(m *InteractiveMode, ctx context.Context, fn func()) {
	done := make(chan struct{})
	m.runOnMain(ctx, func() {
		fn()
		close(done)
	})
	<-done
}

// Enter on slash-prefixed text that is not a registered command, while a run
// streams, queues as a steering message. Interactive mode started a second turn that
// failed with "already processing", lost the text, and flipped the busy
// state to idle while the first run still streamed (MODES-02).
func TestInteractiveSlashTextWhileStreamingSteers(t *testing.T) {
	m, ctx, _ := newStreamingRoutingMode(t)
	m.promptTemplates = []PromptTemplate{{Name: "review", Content: "Review carefully: $ARGUMENTS"}}

	var steering []agent.AgentMessage
	var idle bool
	var chat string
	onLoop(m, ctx, func() {
		m.editor.SetText("/not-a-command also check X")
		_ = m.dispatchKey(ctx, "\r")
		m.editor.SetText("/review the tests")
		_ = m.dispatchKey(ctx, "\r")
	})
	time.Sleep(200 * time.Millisecond)
	onLoop(m, ctx, func() {
		steering, _ = m.agent.PendingMessages()
		idle = m.isIdle
		chat = widthx.StripAnsi(strings.Join(m.chatContainer.Render(100), "\n"))
	})
	if len(steering) != 2 {
		t.Fatalf("steering messages = %d, want 2", len(steering))
	}
	if got := extractAgentMessageText(steering[0]); got != "/not-a-command also check X" {
		t.Fatalf("first steer = %q", got)
	}
	if got := extractAgentMessageText(steering[1]); got != "Review carefully: the tests" {
		t.Fatalf("template steer = %q, want the expanded template", got)
	}
	if idle || !m.turnActive.Load() {
		t.Fatalf("busy state after the steer: isIdle=%v turnActive=%v, want busy", idle, m.turnActive.Load())
	}
	if strings.Contains(chat, "already processing") {
		t.Fatalf("a second turn was started:\n%s", chat)
	}
}

// A session replacement settles the active run first, awaiting the abort,
// so the aborted turn cannot
// persist into the session that replaces it.
func TestSettleActiveRunAbortsAndWaitsForTheRun(t *testing.T) {
	m, ctx, _ := newStreamingRoutingMode(t)
	onLoop(m, ctx, func() {
		if err := m.settleActiveRun(); err != nil {
			t.Error(err)
		}
	})
	if m.runStreaming() {
		t.Fatal("the run was still active after settleActiveRun")
	}
}
