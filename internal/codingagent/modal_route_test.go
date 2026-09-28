package codingagent

import (
	"testing"
	"time"
)

// A chunk the input pump reads before any modal has ever opened (a
// terminal's startup reply) must reach the first modal when one opens while
// the main loop is busy with it; otherwise the pump and the main loop wait on
// each other and no key works again.
func TestInputHeldBeforeAnyModalReachesTheFirstModal(t *testing.T) {
	m := &InteractiveMode{}
	ctx := t.Context()
	readCh := make(chan inputChunk) // the main loop is inside the modal and never reads it
	routed := make(chan struct{})
	go func() {
		m.routeInputChunk(ctx, []byte("\x1b[?62;22c"), readCh)
		close(routed)
	}()
	time.Sleep(50 * time.Millisecond) // the pump now holds the chunk for the main loop

	inputCh, release := m.acquireModalInputChannel()
	defer release()
	select {
	case got := <-inputCh:
		if string(got) != "\x1b[?62;22c" {
			t.Fatalf("modal got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first modal never received the chunk held before it opened: input is deadlocked")
	}
	<-routed
}
