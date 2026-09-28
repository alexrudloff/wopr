package codingagent

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// keybindingsManagerFor builds a default manager for one platform column, so
// a test asserts that column's keys on any host.
func keybindingsManagerFor(platform tui.KeybindingPlatform) *KeybindingsManager {
	km := DefaultKeybindingsManager()
	km.platform = platform
	km.definitions = appKeybindingDefinitionsFor(platform)
	km.rebuild()
	return km
}

// otherColumnKeys is the non-Windows default column the encoding tables assert.
func otherColumnKeys() *KeybindingsManager {
	return keybindingsManagerFor(tui.KeybindingPlatformLinux)
}

func newPendingDisplayHarness(t *testing.T) *InteractiveMode {
	t.Helper()
	model := &ai.Model{ID: "m", DisplayName: "m", Capabilities: ai.ModelCapabilities{ContextWindow: 8000}}
	m := NewInteractiveMode(InteractiveOptions{CWD: t.TempDir(), Model: model})
	m.pendingMessagesContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 100, 30, tui.Options{})
	m.agent = agent.NewAgent(agent.AgentOptions{Model: model})
	m.statusLine = NewStatusLine(model, nil)
	m.editor = tui.NewEditor()
	m.keybindings = otherColumnKeys()
	return m
}

type blockingProvider struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	blockOnce   sync.Once
}

func (p *blockingProvider) ID() string   { return "blocking" }
func (p *blockingProvider) Close() error { return nil }
func (p *blockingProvider) Stream(_ context.Context, _ ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	stream := ai.NewAssistantMessageEventStream()
	partial := &ai.AssistantMessage{Provider: p.ID(), Model: "m", StopReason: ai.StopReasonPending}
	final := &ai.AssistantMessage{Provider: p.ID(), Model: "m", StopReason: ai.StopReasonStop}
	if err := stream.Push(ai.StartEvent{Partial: partial}); err != nil {
		return nil, err
	}
	p.startedOnce.Do(func() { close(p.started) })
	shouldBlock := false
	p.blockOnce.Do(func() { shouldBlock = true })
	go func() {
		if shouldBlock {
			<-p.release
		}
		_ = stream.Push(ai.DoneEvent{Reason: ai.StopReasonStop, Message: final})
	}()
	return stream, nil
}
