package coding

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// warmingProvider answers every request with a 100k-token prompt and records
// each request's options.
type warmingProvider struct {
	mu      sync.Mutex
	options []ai.StreamOptions
}

func (*warmingProvider) ID() string   { return "anthropic" }
func (*warmingProvider) Close() error { return nil }
func (p *warmingProvider) Stream(_ context.Context, _ ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.mu.Lock()
	p.options = append(p.options, options)
	p.mu.Unlock()
	message := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "warm-model",
		Content:    []ai.AssistantContentBlock{ai.TextContent{Text: "done"}},
		Usage:      ai.Usage{Input: 100_000, Output: 1, TotalTokens: 100_001},
		StopReason: ai.StopReasonStop,
	}
	return newSessionTestStream(ai.StartEvent{Partial: message}, ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}), nil
}

func (p *warmingProvider) calls() []ai.StreamOptions {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.options)
}

func warmingModel(provider ai.Provider) *ai.Model {
	return &ai.Model{
		ID: "warm-model", DisplayName: "warm-model", Provider: provider,
		Capabilities: ai.ModelCapabilities{
			ContextWindow: 200_000, InputCostPer1M: 10, OutputCostPer1M: 50,
			CacheReadCostPer1M: 0.25, CacheWriteCostPer1M: 12.5,
		},
		PromptCache:  ai.ModelPromptCache{"short": 300},
		ProviderMeta: ai.ProviderMetadata{ProviderID: "anthropic", API: ai.APIAnthropicMessages},
	}
}

func warmingServices(t *testing.T, mode string) *Services {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("WOPR_HOME", tmp)
	agentDir := filepath.Join(tmp, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"cacheWarming":"`+mode+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	services, err := NewServices(ServicesOptions{CWD: t.TempDir(), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	return services
}

func newWarmingSession(t *testing.T, mode string, options SessionOptions) (*Session, *warmingProvider) {
	t.Helper()
	provider := &warmingProvider{}
	options.Model = warmingModel(provider)
	sess, err := NewSession(warmingServices(t, mode), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, provider
}

// sendAndSettle sends one prompt and drains the Session's events until the
// run's agent_settled, so no event processing races the test.
func sendAndSettle(t *testing.T, sess *Session) {
	t.Helper()
	settled := make(chan struct{})
	go func() {
		for event := range sess.Events() {
			if _, ok := event.(agent.AgentSettledEvent); ok {
				close(settled)
				break
			}
		}
		for range sess.Events() {
		}
	}()
	if _, err := sess.Send(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	<-settled
}

func TestSessionSchedulesCacheWarmingAfterASessionRequest(t *testing.T) {
	sess, _ := newWarmingSession(t, "idle", SessionOptions{NoSession: true})
	sendAndSettle(t, sess)
	status := sess.CacheWarmingStatus()
	if status == nil || status.NextWarmAt <= time.Now().UnixMilli() {
		t.Fatalf("status after request = %+v, want a scheduled refresh", status)
	}

	// Equivalent shallow copies remain current, but removing the request
	// prefix does not.
	sess.Agent().SetMessages(sess.Agent().Messages())
	model := *sess.Agent().Model()
	sess.Agent().SetModel(&model)
	if status := sess.CacheWarmingStatus(); status.NextWarmAt <= time.Now().UnixMilli() {
		t.Fatalf("status after shallow copies = %+v, want still scheduled", status)
	}
	sess.Agent().SetMessages(sess.Agent().Messages()[1:])
	if status := sess.CacheWarmingStatus(); status.Reason != "conversation context changed" {
		t.Fatalf("status after prefix removal = %+v", status)
	}
}

func TestResumedSessionWaitsForTheNextRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.jsonl")
	inner := icodingagent.NewSession("resume", dir)
	inner.SetPath(path)
	if _, err := inner.AppendMessage(agent.AgentMessage{User: &agent.UserMessage{Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "test"}}}}); err != nil {
		t.Fatal(err)
	}
	usage := ai.Usage{Input: 100_000, Output: 1}
	if _, err := inner.AppendMessage(agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: "assistant", Provider: "anthropic", ModelID: "warm-model", Usage: &usage}}); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.AppendUsage("cache_warm", "anthropic", "warm-model", usage, ""); err != nil {
		t.Fatal(err)
	}
	sess, provider := newWarmingSession(t, "idle", SessionOptions{ResumePath: path})
	if len(provider.calls()) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(provider.calls()))
	}
	if status := sess.CacheWarmingStatus(); status == nil || *status != (icodingagent.CacheWarmingStatus{State: "inactive", Reason: "waiting for first request"}) {
		t.Fatalf("status = %+v", status)
	}
}
