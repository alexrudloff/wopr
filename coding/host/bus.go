package host

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Event is one typed bus event. Publish marshals Data exactly once and queues
// the same bytes beside Type for every subscriber.
//
// Type is the SSE event name, set by the producer. The bus never interprets it:
// the vocabulary is the producer's, so no layer translates between an agent
// event and a wire name.
type Event struct {
	Type    string // SSE event name, set by the producer
	Session string // owning session id; "" for server-wide
	Data    any
}

// Bus is the typed event bus. Exported despite having no in-slice caller: the repo's
// `unused` linter rejects unexported dead code and the item names the bus. Unexporting
// this breaks gate 3.
type Bus struct {
	sink        func(Event, []byte)
	mu          sync.Mutex
	subscribers map[uint64]*Subscription
	nextSubID   uint64
}

func newBus(sink func(Event, []byte)) *Bus {
	return &Bus{
		sink:        sink,
		subscribers: make(map[uint64]*Subscription),
	}
}

// Publish marshals Data once and queues the bytes beside Type for the sink and
// for every subscriber, so fan-out is one pointer copy each, never a
// per-subscriber marshal. It returns json.Marshal's error: an unmarshalable
// payload is a producer bug, and the caller drops that event and keeps running
// rather than tearing down every stream.
//
// Publish never blocks. A subscriber whose byte budget is full is dropped, not
// waited for: the producer is an agent goroutine, and one stalled reader must
// not be able to stop every session's events.
//
// The returned payload slice may have spare capacity; subscribers MUST treat
// it as read-only and must not append into it. Doing so corrupts the shared
// backing array for all other subscribers.
func (b *Bus) Publish(ev Event) error {
	payload, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	if b.sink != nil {
		b.sink(ev, payload)
	}

	b.mu.Lock()
	subs := make([]*Subscription, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		sub.Push(ev.Type, payload)
	}
	return nil
}

// Subscribe registers a subscriber whose queue holds at most maxBytes of
// payload plus event-name bytes before the subscriber is dropped. The budget is
// a parameter because it belongs to the transport that drains the queue, not to
// the bus.
func (b *Bus) Subscribe(maxBytes int) *Subscription {
	b.mu.Lock()
	id := b.nextSubID
	b.nextSubID++
	sub := &Subscription{
		bus:    b,
		id:     id,
		bound:  maxBytes,
		ready:  make(chan struct{}, 1),
		doneCh: make(chan struct{}),
	}
	b.subscribers[id] = sub
	b.mu.Unlock()
	return sub
}

// frame is one queued SSE frame's two halves. Publish marshals Data only, so
// the event name cannot be recovered from the payload and must travel with it:
// a subscriber holding bare payload bytes cannot write an `event:` line and
// would hand every frame to the client untyped.
type frame struct {
	name    string
	payload []byte
}

// Subscription is one subscriber's handle: a mutex-guarded FIFO with an exact
// queued-byte counter. doneCh is the field; there is no accessor, because Next
// reports a closed subscription as an error and is the only reader one would
// have.
type Subscription struct {
	bus       *Bus
	id        uint64
	mu        sync.Mutex
	frames    []frame
	queued    int
	bound     int
	dropped   bool
	ready     chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once // guards idempotent Close
}

// Push queues one frame and reports whether it dropped the subscriber. It never
// blocks. The already-queued frames are kept on a drop: the consumer reads the
// prefix it is owed before the terminal frame says where the stream stopped,
// which keeps its view coherent and makes the drop detectable.
func (s *Subscription) Push(name string, payload []byte) bool {
	n := len(name) + len(payload)
	s.mu.Lock()
	if s.dropped {
		s.mu.Unlock()
		return false
	}
	// Two ways to miss the budget, one action: a frame larger than the whole
	// budget, and a frame that does not fit in what is left. No queue can
	// honour the bound in either case, so the only non-lying action is to drop
	// the subscriber. The first branch is unreachable against the SSE budget of
	// 2×maxTailBytes and wopr's 17 MiB largest pinned entry; the guard earns its
	// place because one synthetic oversized frame tests it.
	if n > s.bound || s.queued+n > s.bound {
		s.dropped = true
		s.signal()
		s.mu.Unlock()
		return true
	}
	s.frames = append(s.frames, frame{name: name, payload: payload})
	s.queued += n
	// Signalled under the mutex, not after releasing it: pop drains a token that
	// outlived its frame while holding the same mutex, and it can only tell the
	// two apart because an append and its token are one atomic step.
	s.signal()
	s.mu.Unlock()
	return false
}

// Next pops the next queued frame, waiting up to timeout for one to arrive. A
// timeout of zero or less waits on ctx alone. It reports terminal once a
// dropped subscription's queue has drained, so the caller learns that the stream
// ended at a byte bound and not by choice; the terminal frame is the
// subscriber's own to write, because the bus never sees it.
func (s *Subscription) Next(ctx context.Context, timeout time.Duration) (string, []byte, bool, error) {
	f, terminal, ok := s.pop()
	if ok {
		return f.name, f.payload, false, nil
	}
	if terminal {
		return "", nil, true, nil
	}
	if err := s.wait(ctx, timeout); err != nil {
		return "", nil, false, err
	}
	// A token implies a queued frame or a drop: Push appends and signals under
	// the mutex, pop drains a token it finds with an empty queue under the same
	// mutex, and one subscription has one consumer, so nothing can have taken
	// the frame in between. The drain is what keeps a token from outliving the
	// frame it announced and reporting a deadline that never elapsed.
	f, terminal, ok = s.pop()
	switch {
	case ok:
		return f.name, f.payload, false, nil
	case terminal:
		return "", nil, true, nil
	default:
		return "", nil, false, context.DeadlineExceeded
	}
}

// pop removes the oldest frame under the lock. On an empty queue it reports
// whether the subscription has been dropped, which is the terminal signal, and
// it takes the stale token with it: an empty queue under the lock means every
// frame this token announced has already been delivered, so leaving the token
// behind would turn the next wait into a deadline that never elapsed — a
// heartbeat after every drained burst.
func (s *Subscription) pop() (f frame, terminal, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		select {
		case <-s.ready:
		default:
		}
		return frame{}, s.dropped, false
	}
	f = s.frames[0]
	// Clearing the popped slot releases the payload for the collector. Merely
	// advancing the header leaves it reachable in the backing array, so a
	// connection that drains — never dropped, never over its bound — retains
	// every byte it ever queued and sixteen of them hold the whole 512 MiB.
	s.frames[0] = frame{}
	s.frames = s.frames[1:]
	s.queued -= len(f.name) + len(f.payload)
	return f, false, true
}

// wait blocks until a push signals, the subscription closes, ctx ends, or the
// timeout elapses.
func (s *Subscription) wait(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		select {
		case <-s.ready:
			return nil
		case <-s.doneCh:
			return context.Canceled
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.ready:
		return nil
	case <-timer.C:
		return context.DeadlineExceeded
	case <-s.doneCh:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

// signal wakes the consumer without blocking. ready has capacity one, so a
// coalesced push loses nothing: the consumer drains the whole queue.
func (s *Subscription) signal() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Close removes the subscription from the bus and closes doneCh.
// It does NOT close the queue. Publish snapshots the subscriber list under the
// mutex, then pushes outside it. Discarding the queue would be wrong twice: a
// Publish already past the mutex would push into a released slice, and holding
// the mutex across the push to avoid that is what a blocked producer is
// forbidden to do — Push never blocks, which is the whole reason the FIFO
// replaced the channel. Close is idempotent: safe to call twice, safe to race.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subscribers, s.id)
		s.bus.mu.Unlock()
		close(s.doneCh)
	})
}
