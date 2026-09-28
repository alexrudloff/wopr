package coding

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/alexrudloff/wopr/agent"
)

// sessionRunState tracks the Session's agent run: whether it is active, and
// idle waiters. Every mode reads streaming and idle state from here, and abort
// reaches the active run whichever caller started it.
type sessionRunState struct {
	active atomic.Bool

	mu         sync.Mutex
	generation uint64
	cancel     context.CancelFunc // cancels the active run
	idle       chan struct{}      // closed when the session may have become idle
}

// IsStreaming reports whether an agent run, including its retries,
// compaction, and queued continuations, is active.
func (s *Session) IsStreaming() bool { return s.runState.active.Load() }

// IsIdle reports whether the session has no active run and no compaction in
// flight.
func (s *Session) IsIdle() bool { return !s.IsStreaming() && !s.IsCompacting() }

// HasPendingMessages reports whether steering or follow-up messages are
// queued.
func (s *Session) HasPendingMessages() bool { return s.PendingMessageCount() > 0 }

// WaitForIdle blocks until the session is idle, ctx ends, or the session
// closes.
func (s *Session) WaitForIdle(ctx context.Context) error {
	for {
		s.runState.mu.Lock()
		if s.IsIdle() {
			s.runState.mu.Unlock()
			return nil
		}
		if s.runState.idle == nil {
			s.runState.idle = make(chan struct{})
		}
		idle := s.runState.idle
		s.runState.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closeDone:
			return nil
		}
	}
}

// notifyIdleWaiters wakes WaitForIdle callers to re-check the idle state.
func (s *Session) notifyIdleWaiters() {
	s.runState.mu.Lock()
	if s.runState.idle != nil {
		close(s.runState.idle)
		s.runState.idle = nil
	}
	s.runState.mu.Unlock()
}

// RequestAbort cancels the active run, retry wait, compaction, and branch
// summary without waiting.
func (s *Session) RequestAbort() {
	s.runState.mu.Lock()
	cancel := s.runState.cancel
	s.runState.mu.Unlock()
	s.AbortRetry()
	s.AbortCompaction()
	s.AbortBranchSummary()
	if cancel != nil {
		cancel()
	}
}

// Abort cancels the active run, whoever started it, and waits until the
// session is idle.
func (s *Session) Abort(ctx context.Context) error {
	s.RequestAbort()
	return s.WaitForIdle(ctx)
}

// beginAgentRun marks the run active and returns its cancellable context and
// owned cleanup. A stale run's cleanup cannot
// erase a newer run's cancellation.
func (s *Session) beginAgentRun(ctx context.Context) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ctx)
	s.beginRouteRun()
	s.routeBoundary.Store(true)
	s.runState.mu.Lock()
	s.runState.generation++
	generation := s.runState.generation
	s.runState.cancel = cancel
	s.runState.active.Store(true)
	s.runState.mu.Unlock()
	select {
	case <-s.closeDone:
		cancel()
	default:
	}
	return runCtx, func() {
		cancel()
		s.finishAgentRun(generation)
	}
}

func (s *Session) finishAgentRun(generation uint64) {
	s.runState.mu.Lock()
	if s.runState.generation != generation || !s.runState.active.Load() {
		s.runState.mu.Unlock()
		return
	}
	s.runState.cancel = nil
	s.runState.active.Store(false)
	idle := s.runState.idle
	s.runState.idle = nil
	s.runState.mu.Unlock()
	if idle != nil {
		close(idle)
	}
}

// endAgentRun ends a run whose caller owns agent_settled (RunAgentPrompt).
func (s *Session) endAgentRun(end context.CancelFunc) { end() }

// emitAgentSettled ends the run and emits agent_settled. The run is inactive
// before listeners see the event. The cache warmer is notified first.
func (s *Session) emitAgentSettled() {
	s.OnAgentSettled()
	s.runState.mu.Lock()
	s.runState.cancel = nil
	s.runState.mu.Unlock()
	s.runState.active.Store(false)
	s.emitOrderedEvent(agent.AgentSettledEvent{})
}
