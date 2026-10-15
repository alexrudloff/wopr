package host

import (
	"context"
	"errors"
	"sync"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
)

// Services is the coding.Services type, re-exported so serve can use it
// without importing coding directly.
type Services = coding.Services

// ErrSessionBusy is returned when a session already has a run in flight.
var ErrSessionBusy = errors.New("host: session already has a run in flight")

// ErrDuplicateMessageID is returned when a client-minted entry id already names a
// persisted entry or an in-flight prompt. wopr's transcript is append-only and has
// no upsert, so exactly one entry may carry a given id.
var ErrDuplicateMessageID = errors.New("host: messageID already exists")

// ErrNoModel is returned when StartOptions.Model is not set.
var ErrNoModel = errors.New("host: StartOptions.Model is required")

// Translator maps one session's agent events onto bus events. The wire
// vocabulary and the part-ID helper live in the serve layer, so the mapper does
// too; this package owns the call site and the entry-id resolution the mapper
// needs and cannot compute.
//
// entryID is the transcript entry id for a MessageEndEvent's message, empty for
// every other event. The agent persists a message before its message_end
// reaches any listener, so the id exists by the time the forwarder looks. An
// error reports an event the mapper could not represent; forwardEvents drops
// that event and keeps the run going.
type Translator func(ev agent.AgentEvent, entryID string) ([]Event, error)

// TranslatorFactory builds one Translator per session. The mapper's per-message
// streamed state is per session and needs no lock, because a session's forwarder
// is a single goroutine.
type TranslatorFactory func(sessionID string) Translator

// Host owns every session a served process runs.
type Host struct {
	svcs     *coding.Services
	bus      *Bus
	registry map[string]*Session
	mu       sync.RWMutex

	// Translate maps a session's agent events onto the bus. nil means the
	// forwarder publishes nothing, which is what an RPC-only process wants: it
	// drains Session.Events() itself. Set by the transport that owns the wire
	// vocabulary before the first session starts.
	Translate TranslatorFactory
}

// New returns a Host over svcs. sink, when non-nil, also receives every published
// event's bytes: the single-consumer path, named by the item
// (/home/kc/dev/wopr/BACKLOG.md:39-40). ServeRPC takes *coding.Services directly
// and writes its own JSON lines, so the bus is not on the RPC path.
func New(svcs *coding.Services, sink func(Event, []byte)) *Host {
	h := &Host{
		svcs:     svcs,
		bus:      newBus(sink),
		registry: make(map[string]*Session),
	}
	return h
}

// Services returns the coding.Services for callers that need it.
func (h *Host) Services() *coding.Services {
	return h.svcs
}

// Sessions returns a snapshot of the registry under h.mu.RLock.
func (h *Host) Sessions() []*Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	sessions := make([]*Session, 0, len(h.registry))
	for _, s := range h.registry {
		sessions = append(sessions, s)
	}
	return sessions
}

// StartOptions is what a caller outside coding/host may configure. The model is
// a "provider/model" spec because the host resolves it: no caller here can import
// coding to build an *ai.Model itself.
type StartOptions struct {
	Model      string // required, "provider/model"
	SessionDir string // optional, empty means default dir
}

// Start creates one session and registers it under its id. A duplicate id
// closes the new session and returns an error.
func (h *Host) Start(opts StartOptions) (*Session, error) {
	if opts.Model == "" {
		return nil, ErrNoModel
	}
	model, err := coding.BuildModel(opts.Model, h.svcs)
	if err != nil {
		return nil, err
	}
	sess, err := coding.StartSession(h.svcs, coding.SessionStartOptions{
		Model:      model,
		SessionDir: opts.SessionDir,
	})
	if err != nil {
		return nil, err
	}
	id := sess.ID()
	wrapped := &Session{
		host:        h,
		id:          id,
		sess:        sess,
		inFlightIDs: make(map[string]struct{}),
	}
	h.mu.Lock()
	if _, exists := h.registry[id]; exists {
		h.mu.Unlock()
		_ = sess.Close() // close error dropped; duplicate-id error takes precedence
		return nil, errors.New("host: session id already registered")
	}
	h.registry[id] = wrapped
	h.mu.Unlock()
	go h.forwardEvents(wrapped)
	return wrapped, nil
}

// Publish puts one event on the bus, marshalling Data once for every subscriber.
func (h *Host) Publish(ev Event) error {
	return h.bus.Publish(ev)
}

// Subscribe registers a stream that drains bus events, bounded to maxBytes of
// queued payload plus event-name bytes.
func (h *Host) Subscribe(maxBytes int) *Subscription {
	return h.bus.Subscribe(maxBytes)
}

// forwardEvents drains one session's event channel onto the bus, one goroutine
// per session, so the fan-out runs off the agent's path and no event of a run
// is missed. It exits when the session closes, which closes the channel.
//
// Single-consumer invariant: Session.Events() is one channel. ServeRPC drains
// the same channel for the session it builds itself, so a host-owned session
// must never be handed to ServeRPC — two drains split the stream and messages
// vanish with nothing to show for it. ServeRPC takes *coding.Services directly
// and builds its own session, so the two never overlap today.
//
// AcknowledgeEvent releases the marker FlushEvents sends to wait for this
// consumer. No barrier reaches this loop today, because sendContent — the path
// PromptContent and PromptContentWait take — never calls FlushEvents; it is
// acknowledged anyway because this is the only Events() consumer, it is one
// line, and a barrier nobody acknowledges is a permanent block for the next
// caller that reaches one.
func (h *Host) forwardEvents(sess *Session) {
	var translate Translator
	if h.Translate != nil {
		translate = h.Translate(sess.id)
	}
	for ev := range sess.Inner().Events() {
		if coding.AcknowledgeEvent(ev) {
			continue
		}
		if translate == nil {
			continue
		}
		entryID := ""
		if end, ok := ev.(agent.MessageEndEvent); ok && (end.Message.Assistant != nil || end.Message.User != nil) {
			entryID, _ = sess.Inner().MessageEntryID(end.Message)
		}
		events, err := translate(ev, entryID)
		if err != nil {
			continue
		}
		for _, out := range events {
			_ = h.Publish(out)
		}
	}
}

// Get returns a registered session by id, or nil if not found.
func (h *Host) Get(id string) (*Session, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.registry[id]
	return s, ok
}

// queuedPrompt is a prompt accepted while the run slot was held, carrying the
// client-minted entry id so it survives until the prompt runs.
type queuedPrompt struct {
	content []ai.UserContentBlock
	entryID string
	before  func() error
}

// Session is one session the host owns. It does not embed *coding.Session: an
// exported field would let a caller reach Send and bypass the run rule below.
type Session struct {
	host *Host
	id   string
	sess *coding.Session
	run  sync.Mutex

	// queueMu serializes accepting a prompt and handing the run slot off, so a
	// prompt cannot be appended after s.run is released but before the releasing
	// run observes it. queued holds prompts that arrived while the slot was held,
	// oldest first.
	queueMu sync.Mutex
	queued  []queuedPrompt

	// idsMu guards inFlightIDs: client-minted entry ids reserved by an accepted
	// prompt that has not yet settled. It is a leaf lock: it may be taken while
	// run is held (releaseEntryID runs under runAndRelease before releaseRun
	// unlocks), but no path acquires run or queueMu while holding idsMu, so it
	// cannot invert with the queueMu→run (PromptContent) / run→queueMu
	// (releaseSlot) pair.
	idsMu       sync.Mutex
	inFlightIDs map[string]struct{}

	// cancel aborts the reserved run — the run this Session started on its own
	// reservation — from that reservation until releaseRun clears it. A non-nil
	// cancel therefore means a reserved run is outstanding and has not yet been
	// released by releaseRun, not that it is still active. A queued run handed
	// the slot by releaseSlot is deliberately not registered here and is
	// abortable only from beginAgentRun onward via RequestAbort.
	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// handoffBarrier, when non-nil, runs in releaseSlot after the handed-off run's
	// context is created and before its goroutine starts. Tests set it to execute an
	// Abort inside the handoff window; production leaves it nil.
	handoffBarrier func()
}

func (s *Session) ID() string {
	return s.id
}

// Inner returns the underlying *coding.Session.
// This is the deliberate, documented escape hatch for the read surface;
// serve never calls Send on it.
func (s *Session) Inner() *coding.Session {
	return s.sess
}

// reserveEntryID claims a client-minted entry id for a prompt about to be accepted.
// The claim and the check are one critical section over both the persisted entry
// map and the in-flight set, so two same-id requests arriving before the first
// persists cannot both pass. An empty id is a no-op: server-minted ids cannot
// collide with a client resend.
func (s *Session) reserveEntryID(id string) error {
	if id == "" {
		return nil
	}
	s.idsMu.Lock()
	defer s.idsMu.Unlock()
	if _, exists := s.sess.EntryByID(id); exists {
		return ErrDuplicateMessageID
	}
	if _, exists := s.inFlightIDs[id]; exists {
		return ErrDuplicateMessageID
	}
	s.inFlightIDs[id] = struct{}{}
	return nil
}

// releaseEntryID drops a reservation after persistence (or after acceptance failed
// before a run owned it), so a later resend is rejected by the persisted check.
func (s *Session) releaseEntryID(id string) {
	if id == "" {
		return
	}
	s.idsMu.Lock()
	delete(s.inFlightIDs, id)
	s.idsMu.Unlock()
}

// Prompt sends a prompt to the session. It returns ErrSessionBusy if the
// session already has a run in flight.
func (s *Session) Prompt(ctx context.Context, text string) error {
	if !s.run.TryLock() {
		return ErrSessionBusy
	}
	defer s.releaseSlot()
	_, err := s.sess.Send(ctx, text)
	return err
}

// PromptContent reserves the session's single run slot and starts the run on a
// goroutine it owns, returning as soon as the reservation lands. A taken slot
// queues content to run when the current run settles, so a busy session returns
// nil after queueing. It returns ErrDuplicateMessageID when entryID already names
// a persisted entry or an in-flight prompt. Everything after the reservation is
// reported on the event bus.
//
// entryID, when non-empty, is the client-minted transcript entry id the user
// message persists under.
//
// ctx is detached inside this method: an accepted run outlives its caller, and
// an http.Request.Context is cancelled the moment the handler returns, which
// would kill the run this just accepted while the caller still answered 204.
//
// before, when non-nil, runs once this run holds the slot and before the turn
// starts, a queued one included. A caller that mutates session-wide state needs
// it there: a model swap applied at accept time changes the model of the turn
// already running, which is why the queued prompt carries its own. A before
// error abandons the turn; the caller has already been answered, so the run
// ends as it would after any other failed start.
func (s *Session) PromptContent(ctx context.Context, content []ai.UserContentBlock, entryID string, before func() error) error {
	if err := s.reserveEntryID(entryID); err != nil {
		return err
	}
	s.queueMu.Lock()
	if !s.run.TryLock() {
		s.queued = append(s.queued, queuedPrompt{content: content, entryID: entryID, before: before})
		s.queueMu.Unlock()
		return nil
	}
	runCtx, cancel := s.ownRun(ctx) // set s.cancel before returning 204
	s.queueMu.Unlock()
	go s.runAndRelease(runCtx, cancel, content, entryID, before)
	return nil
}

// runAndRelease runs one prompt to settlement, then releases the slot.
// releaseRun is deferred so a panic cannot wedge the run slot, and it is what
// hands a queued prompt the slot when before abandons this turn. releaseEntryID
// is deferred after releaseRun, so it runs first (LIFO) once
// SendContentWithPreflight has returned: an id whose run persisted a user entry
// is still rejected afterward by the persisted EntryByID check, and an id whose
// run ended before persisting is freed for a retry.
func (s *Session) runAndRelease(ctx context.Context, cancel context.CancelFunc, content []ai.UserContentBlock, entryID string, before func() error) {
	defer s.releaseRun(cancel)
	defer s.releaseEntryID(entryID)
	if before != nil && before() != nil {
		return
	}
	_, _ = s.sess.SendContentWithPreflight(ctx, content, entryID, nil)
}

// releaseSlot gives the slot to the next queued prompt instead of leaving it
// idle. Callers hold s.run.
func (s *Session) releaseSlot() {
	s.queueMu.Lock()
	if len(s.queued) > 0 {
		next := s.queued[0]
		s.queued[0] = queuedPrompt{} // drop the backing array's reference to the run content
		s.queued = s.queued[1:]
		runCtx, cancel := context.WithCancel(context.Background())
		s.queueMu.Unlock()
		if s.handoffBarrier != nil {
			s.handoffBarrier() // test-only; nil in production
		}
		go s.runAndRelease(runCtx, cancel, next.content, next.entryID, next.before)
		return
	}
	s.run.Unlock() // under queueMu, paired with PromptContent's TryLock under queueMu
	s.queueMu.Unlock()
}

// PromptContentWait reserves the same single run slot and blocks until the run
// settles, returning the messages it produced. It returns ErrSessionBusy when
// the slot is taken and ErrDuplicateMessageID when entryID already names a
// persisted entry or an in-flight prompt. ctx is detached for the same reason as
// PromptContent's; the caller abandons the wait on its own cancellation. entryID,
// when non-empty, is the client-minted transcript entry id the user message
// persists under.
//
// before, when non-nil, runs once the slot is held and before the turn starts.
// A caller that mutates session-wide state needs it there: a model swap applied
// before the reservation swaps the model of a session that is about to be
// refused. A before error aborts the turn, releases the slot, and is returned
// as is.
func (s *Session) PromptContentWait(ctx context.Context, content []ai.UserContentBlock, entryID string, before func() error) ([]agent.AgentMessage, error) {
	if err := s.reserveEntryID(entryID); err != nil {
		return nil, err
	}
	defer s.releaseEntryID(entryID) // covers ErrSessionBusy, before-failure, and success
	if !s.run.TryLock() {
		return nil, ErrSessionBusy
	}
	if before != nil {
		if err := s.withReservation(before); err != nil {
			return nil, err
		}
	}
	runCtx, cancel := s.ownRun(ctx)
	defer s.releaseRun(cancel)
	return s.sess.SendContentWithPreflight(runCtx, content, entryID, nil)
}

// withReservation runs before with the run slot held and gives the slot back if
// it fails. The release is deferred rather than a statement on the error path so
// a panic in caller code cannot wedge the slot for every later prompt. On
// success the slot stays held and belongs to releaseRun.
func (s *Session) withReservation(before func() error) error {
	keepSlot := false
	defer func() {
		if !keepSlot {
			s.releaseSlot()
		}
	}()
	if err := before(); err != nil {
		return err
	}
	keepSlot = true
	return nil
}

// ownRun detaches ctx from its cancellation, makes it cancellable, and
// registers the cancel for Abort. Only the reserved run is registered; a queued
// run handed the slot by releaseSlot uses a bare context.WithCancel and is not
// registered. A cancel delivered before beginAgentRun still stops the run,
// because context.WithCancel over an already-cancelled parent yields an
// already-cancelled child.
func (s *Session) ownRun(ctx context.Context) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancelMu.Lock()
	s.cancel = cancel
	s.cancelMu.Unlock()
	return runCtx, cancel
}

// releaseRun clears the stored cancel and returns the run slot to the queue.
func (s *Session) releaseRun(cancel context.CancelFunc) {
	s.cancelMu.Lock()
	s.cancel = nil
	s.cancelMu.Unlock()
	cancel()
	s.releaseSlot()
}

// Abort stops the run this Session owns. A non-nil host cancel means a reserved
// run is outstanding and has not yet been released by releaseRun: cancel it and
// return. The host run context parents beginAgentRun, the retry context, and the
// threshold/overflow compaction context, so cancel() stops all three, and
// returning without RequestAbort keeps a late abort from landing on a run that
// has since begun. With no host cancel, a run handed the slot by releaseSlot is
// reached only through coding's active run: IsStreaming is false exactly while
// no run is between beginAgentRun and settle, so an abort raised in the handoff
// window (the queued run not yet begun) returns without cancelling it, while an
// abort of an active handed-off run cancels it via RequestAbort. It does not
// wait for the drain: the cancel has already been delivered, and a later
// session.status reports idle. Aborting an idle session is success.
func (s *Session) Abort() {
	s.cancelMu.Lock()
	cancel := s.cancel
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
		return
	}
	if !s.sess.IsStreaming() {
		return
	}
	s.sess.RequestAbort()
}

// Close deregisters the session before closing, so a closed session cannot be
// fetched from the registry and the map does not accumulate dead entries.
func (s *Session) Close() error {
	s.host.mu.Lock()
	delete(s.host.registry, s.id)
	s.host.mu.Unlock()
	return s.sess.Close()
}
