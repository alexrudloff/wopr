package coding

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// Cache warming wiring: the CacheWarmer each Session gets, the StreamFn that
// restarts it, and its settle, dispose, status, and mode hooks.

// sessionCacheWarming holds the warmer bound to the current inner Session. Retired warmers are owned only until their cancelled refreshes drain; Close joins those tasks without retaining completed Sessions.
type sessionCacheWarming struct {
	mu        sync.Mutex
	warmer    *icodingagent.CacheWarmer
	sessionID string
	closed    bool
	retired   sync.WaitGroup
}

// cacheWarmingStreamFn sends each agent request through the model's provider
// after restarting cache warming for it. It refuses a model the routing
// mode forbids (see objectiveGate).
func cacheWarmingStreamFn(session func() *Session) agent.StreamFn {
	return func(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
		if s := session(); s != nil {
			if err := s.RouteError(); err != nil {
				return nil, err
			}
			if err := s.objectiveGate(model); err != nil {
				return nil, err
			}
			s.startCacheWarming(model, transcript, options)
		}
		return model.Provider.Stream(ctx, transcript, options)
	}
}

// streamWarmRequest replays a warm request directly through the provider,
// never through the agent's StreamFn.
func streamWarmRequest(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	return model.Provider.Stream(ctx, transcript, options)
}

// installCacheWarmer binds a fresh warmer to inner and retires the previous
// one.
func (s *Session) installCacheWarmer(inner *icodingagent.Session) {
	s.warming.mu.Lock()
	defer s.warming.mu.Unlock()
	if s.warming.closed {
		return
	}
	if previous := s.warming.warmer; previous != nil {
		previousID := s.warming.sessionID
		previous.Close()
		s.warming.retired.Go(func() {
			previous.Wait()
			s.cleanupRetiredSessionResources(previousID)
		})
	}
	warmer := icodingagent.NewCacheWarmer(streamWarmRequest, inner, s.services.SettingsManager().GetCacheWarmingMode)
	warmer.SetOnWarmed(s.emitEntryAppended)
	s.warming.warmer, s.warming.sessionID = warmer, inner.ID()
}

// cleanupRetiredSessionResources releases provider resources owned by a
// replaced inner Session when the outgoing session is disposed. It runs after the
// retired refresh drains, so a late provider completion cannot recreate a
// resource afterwards. When the retired ID is the current Session's ID again
// (same-ID replacement or a switch back), the resources are the successor's,
// and Close owns their cleanup. The check and cleanup hold warming.mu so a
// concurrent replacement cannot adopt that ID in between.
func (s *Session) cleanupRetiredSessionResources(sessionID string) {
	s.warming.mu.Lock()
	defer s.warming.mu.Unlock()
	if sessionID == s.warming.sessionID {
		return
	}
	ai.CloseOpenAICodexWebSocketSessions(sessionID)
	if s.temp != nil {
		s.noteTempCleaned(s.temp.CleanSession(sessionID, true))
	}
}

func (s *Session) cacheWarmer() (*icodingagent.CacheWarmer, string) {
	s.warming.mu.Lock()
	defer s.warming.mu.Unlock()
	return s.warming.warmer, s.warming.sessionID
}

// startCacheWarming restarts warming from a session request. Compaction and
// summaries use their own paths, so only session requests replace the cache
// entry.
func (s *Session) startCacheWarming(model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) {
	warmer, sessionID := s.cacheWarmer()
	if warmer == nil || options.SessionID != sessionID {
		return
	}
	request := icodingagent.CacheWarmRequest{Model: model, Context: transcript, Options: options}
	warmer.Start(request, s.cacheContextIsCurrent(model))
}

// cacheContextIsCurrent keeps warming while the current transcript still
// extends the request's prefix on the same model. Messages compare by
// identity; a copied message list with
// the same messages stays current.
func (s *Session) cacheContextIsCurrent(requestModel *ai.Model) func() bool {
	messages := s.agent.MessagesSnapshot()
	return func() bool {
		current := s.agent.Model()
		if current == nil || providerID(current) != providerID(requestModel) || current.ID != requestModel.ID {
			return false
		}
		currentMessages := s.agent.MessagesSnapshot()
		return len(messages) <= len(currentMessages) && slices.EqualFunc(messages, currentMessages[:len(messages)], sameAgentMessage)
	}
}

func sameAgentMessage(a, b agent.AgentMessage) bool {
	return a.System == b.System && a.User == b.User && a.Assistant == b.Assistant && a.ToolResult == b.ToolResult &&
		reflect.ValueOf(a.Custom).UnsafePointer() == reflect.ValueOf(b.Custom).UnsafePointer()
}

// emitEntryAppended reports a persisted cache-warming usage entry on the
// session event stream from the warmer's onWarmed callback. It
// gives up as soon as ctx ends: the warmer was closed, and its Close is
// waiting for this call to return before it does. The actual send reuses
// emitOrderedEvent's already-reviewed funnel/recover, run in a background
// goroutine bounded by s.closeDone, so a full channel during a warmer
// replacement (ctx cancelled, s.closeDone not yet closed) cannot make Close
// wait on channel capacity: the goroutine keeps trying to deliver the event
// until either it succeeds or the session itself closes.
func (s *Session) emitEntryAppended(ctx context.Context, entry icodingagent.UsageEntry) {
	raw, err := json.Marshal(entry)
	if err != nil || ctx.Err() != nil {
		return
	}
	sent := make(chan struct{})
	go func() {
		s.emitOrderedEvent(agent.EntryAppendedEvent{Entry: raw})
		close(sent)
	}()
	select {
	case <-ctx.Done():
	case <-sent:
	}
}

// CacheWarmingStatus reports the current cache-warming state and the policy
// inputs that produced it, or nil when the Session has no warmer.
func (s *Session) CacheWarmingStatus() *icodingagent.CacheWarmingStatus {
	warmer, _ := s.cacheWarmer()
	if warmer == nil {
		return nil
	}
	status := warmer.Status()
	return &status
}

// SetCacheWarmingMode persists the cache-warming mode and immediately
// reconciles active warming.
func (s *Session) SetCacheWarmingMode(mode icodingagent.CacheWarmingMode) error {
	if err := s.services.SettingsManager().SetCacheWarmingMode(mode); err != nil {
		return err
	}
	if warmer, _ := s.cacheWarmer(); warmer != nil {
		warmer.OnModeChanged()
	}
	return nil
}

// OnAgentSettled tells the cache warmer that an agent run settled, the first
// step of settling a run.
func (s *Session) OnAgentSettled() {
	if warmer, _ := s.cacheWarmer(); warmer != nil {
		warmer.OnAgentSettled()
	}
}

// closeCacheWarming cancels the current warmer and prevents further installations. Every retired warmer is already cancelled.
func (s *Session) closeCacheWarming() {
	s.warming.mu.Lock()
	s.warming.closed = true
	warmer := s.warming.warmer
	s.warming.mu.Unlock()
	if warmer != nil {
		warmer.Close()
	}
}

// waitCacheWarming drains the current and retired warmers after closeCacheWarming prevents new work.
func (s *Session) waitCacheWarming() {
	if warmer, _ := s.cacheWarmer(); warmer != nil {
		warmer.Wait()
	}
	s.warming.retired.Wait()
}
