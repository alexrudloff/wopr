package codingagent

import (
	"context"
	"slices"
	"sync"
)

// terminalInputListener is one raw terminal-input listener, kept in
// registration order. It answers
// synchronously on the owner loop.
type terminalInputListener struct {
	id      uint64
	handler func(data string) (consume bool)
}

// inputChunk is one parsed terminal sequence routed to the owner loop. The
// loop settles ticket once the chunk's terminal-input listeners are done.
type inputChunk struct {
	data   []byte
	ticket *inputTicket
}

type inputTicketState uint8

const (
	// inputTicketRouted: the owner loop is handling the chunk.
	inputTicketRouted inputTicketState = iota
	// inputTicketSettled: the listeners are done; the next chunk may follow.
	inputTicketSettled
)

// inputTicket follows one chunk from the input pump to the owner loop until its
// terminal-input listeners settle. The pump routes nothing after the chunk
// until then, so listeners see chunks, and the owner loop handles them, in
// input order. A nil ticket belongs to
// a chunk no pump is waiting on.
type inputTicket struct {
	mu    sync.Mutex
	state inputTicketState
	// done closes when the chunk settles.
	done chan struct{}
}

func newInputTicket() *inputTicket {
	return &inputTicket{done: make(chan struct{})}
}

// settle releases the pump to route the next chunk. It does nothing after the
// chunk settled.
func (t *inputTicket) settle() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != inputTicketRouted {
		return
	}
	t.state = inputTicketSettled
	close(t.done)
}

// passTerminalInput runs data through the terminal-input listeners, then gives
// handle what they pass on and returns handle's error.
func (m *InteractiveMode) passTerminalInput(ctx context.Context, data string, ticket *inputTicket, handle func(context.Context, string) error) error {
	m.terminalInputMu.Lock()
	listeners := slices.Clone(m.terminalInputListeners)
	m.terminalInputMu.Unlock()
	// A listener that consumes the chunk ends the pass.
	consumed := slices.ContainsFunc(listeners, func(listener terminalInputListener) bool { return listener.handler(data) })
	ticket.settle()
	if consumed {
		return nil
	}
	return handle(ctx, data)
}

// inputBacklog is the input pump's ordered queue: parsed input not yet routed,
// and the routed chunk whose terminal-input listeners have not settled.
type inputBacklog struct {
	held    []string
	waiting *inputTicket
}
