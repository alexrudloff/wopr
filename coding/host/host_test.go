package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/ai/aitest"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
)

func TestMain(m *testing.M) {
	// Mirror coding/testmain_test.go:15-49
	testRoot, err := os.MkdirTemp("", "wopr-host-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test config root:", err)
		os.Exit(2)
	}
	// Unset WOPR_CODING_AGENT_DIR and WOPR_CODING_AGENT_SESSION_DIR
	for _, key := range []string{"WOPR_CODING_AGENT_DIR", "WOPR_CODING_AGENT_SESSION_DIR"} {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "clear inherited %s: %v\n", key, err)
			_ = os.RemoveAll(testRoot)
			os.Exit(2)
		}
	}
	// Unset every key from ai.ProviderCredentialEnvVars() and web.SearchEnvVars()
	for _, key := range append(ai.ProviderCredentialEnvVars(), web.SearchEnvVars()...) {
		_ = os.Unsetenv(key)
	}
	// Apply aitest.HermeticCredentialEnv so no cloud SDK chain reaches instance metadata
	for key, value := range aitest.HermeticCredentialEnv(testRoot) {
		_ = os.Setenv(key, value)
	}
	// Point WOPR_HOME at a temp dir removed afterwards
	if err := os.Setenv("WOPR_HOME", filepath.Join(testRoot, "wopr")); err != nil {
		fmt.Fprintln(os.Stderr, "set isolated WOPR_HOME:", err)
		_ = os.RemoveAll(testRoot)
		os.Exit(2)
	}
	code := m.Run()
	if err := os.RemoveAll(testRoot); err != nil {
		fmt.Fprintln(os.Stderr, "remove isolated test config root:", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}

// TestHostRejectsASecondConcurrentPromptPerSession verifies that with a prompt
// in flight on session A, A.Prompt returns ErrSessionBusy, while B.Prompt for a
// different session is accepted and reaches the provider.
func TestHostRejectsASecondConcurrentPromptPerSession(t *testing.T) {
	// Create a blocking HTTP server that signals on entry and blocks until released
	entryCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{})
	releaseOnce := sync.Once{}
	blockingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entryCh <- struct{}{}
		<-releaseCh
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			_, _ = io.WriteString(w, line+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer blockingSrv.Close()
	defer releaseOnce.Do(func() { close(releaseCh) })

	// Create temp dir for WOPR_HOME and models.json
	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a models.json with anthropic entry pointing to our blocking server
	modelsContent := `{"providers":{"anthropic":{"baseUrl":"` + blockingSrv.URL + `","apiKey":"test-key","modelOverrides":{"test-model":{"promptCache":{"short":12}}}}}}`
	modelsJSON := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(modelsJSON, []byte(modelsContent), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	// Build services with our temp dir
	svcs, err := coding.NewServices(coding.ServicesOptions{
		AgentDir: agentDir,
	})
	if err != nil {
		t.Fatalf("NewServices: %v", err)
	}

	// Build model by spec using our test server
	_, err = coding.BuildModel("anthropic/test-model", svcs)
	if err != nil {
		t.Fatalf("BuildModel: %v", err)
	}

	// Create host
	h := New(svcs, nil)

	// Start session A
	sessA, err := h.Start(StartOptions{Model: "anthropic/test-model"})
	if err != nil {
		t.Fatalf("Start session A: %v", err)
	}
	defer func() { _ = sessA.Close() }()

	// Start session B
	sessB, err := h.Start(StartOptions{Model: "anthropic/test-model"})
	if err != nil {
		t.Fatalf("Start session B: %v", err)
	}
	defer func() { _ = sessB.Close() }()

	// Start a prompt on session A that will block
	var wg sync.WaitGroup
	wg.Go(func() {
		// This will block until releaseCh is closed
		_ = sessA.Prompt(context.Background(), "first prompt")
	})

	// Wait for the request to reach the blocking server
	select {
	case <-entryCh:
		// Good, the first request is in flight
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first request to reach server")
	}

	// Now try a second prompt on session A - should return ErrSessionBusy immediately
	contended := make(chan error, 1)
	wg.Go(func() {
		contended <- sessA.Prompt(context.Background(), "second prompt")
	})
	select {
	case err = <-contended:
	case <-time.After(5 * time.Second):
		t.Fatal("second prompt on session A never returned; the run rule is not rejecting it")
	}
	if !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("expected ErrSessionBusy, got %v", err)
	}

	// Now try a prompt on session B - should be accepted and reach the provider
	var sessBErr error
	wg.Go(func() {
		sessBErr = sessB.Prompt(context.Background(), "prompt on B")
	})

	// Wait for session B's request to reach the server
	select {
	case <-entryCh:
		// Session B's request also reached the server
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session B request to reach server")
	}

	// Release the blocking server so both requests can complete
	releaseOnce.Do(func() { close(releaseCh) })

	// Wait for both goroutines to finish
	wg.Wait()

	// Session B's prompt should not have returned ErrSessionBusy
	if errors.Is(sessBErr, ErrSessionBusy) {
		t.Fatalf("session B should not be busy, got %v", sessBErr)
	}
}

// TestAbortInHandoffWindowDoesNotCancelQueuedPrompt proves an Abort that runs in
// the window after A settles and before the queued B enters beginAgentRun does
// not cancel B. The handoffBarrier fires Abort() synchronously between B's
// context creation and B's goroutine start; B must still reach the provider and
// settle. Without the fix, releaseSlot registers B's context as s.cancel, the
// barrier cancels it, and B never reaches the provider.
func TestAbortInHandoffWindowDoesNotCancelQueuedPrompt(t *testing.T) {
	aRelease := make(chan struct{})
	bRelease := make(chan struct{})
	releases := []chan struct{}{aRelease, bRelease}
	arrived := make(chan int, 4)

	var bodiesMu sync.Mutex
	var bodies []string
	var reqN atomic.Int32

	blockingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		idx := int(reqN.Add(1) - 1)
		bodiesMu.Lock()
		bodies = append(bodies, string(body))
		bodiesMu.Unlock()
		arrived <- idx
		if idx < len(releases) {
			// Selecting on the request context lets a cancelled run end here
			// instead of blocking forever.
			select {
			case <-releases[idx]:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			_, _ = io.WriteString(w, line+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer blockingSrv.Close()
	var aOnce, bOnce sync.Once
	defer aOnce.Do(func() { close(aRelease) })
	defer bOnce.Do(func() { close(bRelease) })

	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsContent := `{"providers":{"anthropic":{"baseUrl":"` + blockingSrv.URL + `","apiKey":"test-key","modelOverrides":{"test-model":{"promptCache":{"short":12}}}}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsContent), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	svcs, err := coding.NewServices(coding.ServicesOptions{AgentDir: agentDir})
	if err != nil {
		t.Fatalf("NewServices: %v", err)
	}
	if _, err := coding.BuildModel("anthropic/test-model", svcs); err != nil {
		t.Fatalf("BuildModel: %v", err)
	}

	h := New(svcs, nil)
	sessA, err := h.Start(StartOptions{Model: "anthropic/test-model"})
	if err != nil {
		t.Fatalf("Start session A: %v", err)
	}
	defer func() { _ = sessA.Close() }()

	// Publication: assigned before the first PromptContent, whose
	// `go s.runAndRelease` starts A's goroutine (happens-before). releaseSlot
	// runs in that goroutine and reads a published value.
	sessA.handoffBarrier = func() { sessA.Abort() }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A: accepted, blocks in the provider, holds the slot.
	if err := sessA.PromptContent(ctx, coding.BuildUserContent("A", nil), "", nil); err != nil {
		t.Fatalf("PromptContent A: %v", err)
	}
	waitArrived(t, arrived, 0)

	// B: queued behind A, must be accepted.
	if err := sessA.PromptContent(ctx, coding.BuildUserContent("B", nil), "", nil); err != nil {
		t.Fatalf("PromptContent B: %v", err)
	}

	// A settles -> releaseRun -> releaseSlot -> barrier Abort() in the handoff
	// window -> B's goroutine starts.
	aOnce.Do(func() { close(aRelease) })

	waitArrived(t, arrived, 1)
	bOnce.Do(func() { close(bRelease) })

	if err := sessA.Inner().WaitForIdle(ctx); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}

	msgs := sessA.Inner().Messages()
	var userTexts []string
	sawB := false
	assistantAfterB := false
	for _, m := range msgs {
		switch {
		case m.User != nil:
			var sb strings.Builder
			for _, block := range m.User.Content {
				if tc, ok := block.(ai.TextContent); ok {
					sb.WriteString(tc.Text)
				}
			}
			text := sb.String()
			userTexts = append(userTexts, text)
			if strings.Contains(text, "B") {
				sawB = true
			}
		case m.Assistant != nil:
			if sawB {
				assistantAfterB = true
			}
		}
	}
	if !sawB {
		t.Fatalf("expected a user message containing B, got user texts %q", userTexts)
	}
	if !assistantAfterB {
		t.Fatalf("expected an assistant message after the B user message")
	}

	bodiesMu.Lock()
	gotBodies := append([]string(nil), bodies...)
	bodiesMu.Unlock()
	if len(gotBodies) < 2 {
		t.Fatalf("expected both A and B to reach the provider, got %d requests", len(gotBodies))
	}
	if !strings.Contains(gotBodies[0], `"text":"A"`) {
		t.Errorf("provider's first request did not carry A: %q", gotBodies[0])
	}
	sawBInBody := false
	for _, b := range gotBodies {
		if strings.Contains(b, `"text":"B"`) {
			sawBInBody = true
		}
	}
	if !sawBInBody {
		t.Errorf("provider never saw a request carrying B; bodies=%q", gotBodies)
	}
}

// waitArrived blocks until the provider signals request idx, or the test times
// out. It drains any earlier signals so callers can wait for 0 then 1 in order.
func waitArrived(t *testing.T, arrived <-chan int, want int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-arrived:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for provider request %d", want)
		}
	}
}

// TestSubscriptionCloseIdempotent verifies that Close is safe to call twice and
// safe to race, and that Publish after Close does not panic.
func TestSubscriptionCloseIdempotent(t *testing.T) {
	b := newBus(nil)
	sub := b.Subscribe(1 << 20)

	// Double close must not panic
	sub.Close()
	sub.Close()

	// Concurrent close must not panic
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			sub.Close()
		})
	}
	wg.Wait()

	// Publish after Close must not panic
	if err := b.Publish(Event{Type: "test", Session: "", Data: "payload"}); err != nil {
		t.Fatalf("Publish after Close: %v", err)
	}
}

// TestDrainedBurstDoesNotFakeADeadline pins what the Next timeout means: the
// heartbeat interval, not the gap between two frames. Push signals under the
// mutex and pop takes a token it finds with an empty queue under the same mutex,
// so the token the last drained frame left behind cannot wake the next wait.
// Without that the wait returns at once with a deadline that never elapsed, and
// a stream that has caught up reports one after every burst.
func TestDrainedBurstDoesNotFakeADeadline(t *testing.T) {
	b := newBus(nil)
	sub := b.Subscribe(1 << 20)
	defer sub.Close()

	for range 8 {
		if sub.Push("message.part.delta", []byte(`{"delta":"x"}`)) {
			t.Fatal("a small burst must not drop the subscriber")
		}
	}
	for range 8 {
		if _, _, terminal, err := sub.Next(context.Background(), time.Second); err != nil || terminal {
			t.Fatalf("draining the burst: err=%v terminal=%v", err, terminal)
		}
	}

	// Nothing is queued, so the wait has to run out its timeout. Anything quicker
	// means it woke on a token for a frame already delivered.
	start := time.Now()
	if _, _, terminal, err := sub.Next(context.Background(), 300*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || terminal {
		t.Fatalf("an empty subscription must report the deadline, got terminal=%v err=%v", terminal, err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("the wait returned after %v, so it woke on a stale token instead of the timeout", elapsed)
	}

	// The other branch: a token that carries work is what wakes a wait early.
	if sub.Push("message.part.delta", []byte(`{"delta":"y"}`)) {
		t.Fatal("a small frame must not drop the subscriber")
	}
	name, body, terminal, err := sub.Next(context.Background(), time.Second)
	if err != nil || terminal {
		t.Fatalf("a push after the drain must wake the wait: err=%v terminal=%v", err, terminal)
	}
	if name != "message.part.delta" || string(body) != `{"delta":"y"}` {
		t.Errorf("expected the pushed frame, got %q %q", name, body)
	}
}

// TestServeFanoutDropsStalledSubscribers is the assertion a benchmark cannot
// make: four connections that stall are dropped at the byte bound, and each one
// is told so. Without the terminal signal a dropped subscriber waits forever on
// a connection that will never deliver again, and the client's view stops
// without a marker of where it did.
func TestServeFanoutDropsStalledSubscribers(t *testing.T) {
	const bound = 1 << 10
	b := newBus(nil)
	subs := make([]*Subscription, 4)
	for i := range subs {
		subs[i] = b.Subscribe(bound)
		defer subs[i].Close()
	}

	// Nobody calls Next, so every frame queues until the budget is gone. The
	// payload is 29 bytes and the name 18, so 47 bytes a frame and the budget
	// holds 21 frames.
	payload := []byte(`{"sessionID":"s","delta":"x"}`)
	dropped := 0
	for range 200 {
		for _, sub := range subs {
			if sub.Push("message.part.delta", payload) {
				dropped++
			}
		}
		if dropped == len(subs) {
			break
		}
	}
	if dropped != len(subs) {
		t.Fatalf("expected all %d stalled subscribers dropped, %d dropped", len(subs), dropped)
	}

	for i, sub := range subs {
		delivered := 0
		lastName := ""
		for {
			name, _, terminal, err := sub.Next(context.Background(), time.Second)
			if err != nil {
				t.Fatalf("subscriber %d: draining the queued prefix: %v", i, err)
			}
			if terminal {
				break
			}
			delivered++
			lastName = name
		}
		if delivered == 0 {
			t.Errorf("subscriber %d: expected the already-queued prefix before the terminal frame", i)
		}
		if lastName != "message.part.delta" {
			t.Errorf("subscriber %d: expected the event name beside the payload, got %q", i, lastName)
		}
		// The terminal signal is the end of the stream, so it must not repeat
		// with a frame behind it or vanish on the next read.
		name, body, terminal, err := sub.Next(context.Background(), time.Second)
		if err != nil || !terminal || name != "" || body != nil {
			t.Errorf("subscriber %d: expected terminal with no frame after the drain, got %q %q terminal=%v err=%v", i, name, body, terminal, err)
		}
	}

	// A frame larger than the whole budget cannot be queued by any queue, so it
	// drops the subscriber on its own. Unreachable at the SSE bound of 32 MiB
	// against wopr's 17 MiB largest pinned entry; kept because it has teeth.
	oversized := newBus(nil)
	big := oversized.Subscribe(64)
	defer big.Close()
	if !big.Push("message.part.updated", make([]byte, 65)) {
		t.Error("a frame larger than the whole budget must drop the subscriber")
	}
	if _, _, terminal, err := big.Next(context.Background(), time.Second); err != nil || !terminal {
		t.Errorf("oversized frame: expected the terminal signal, got terminal=%v err=%v", terminal, err)
	}
}

// BenchmarkServeFanout measures the fan-out and the drop path together, with
// -benchmem. Four connections' readers stall for the whole iteration, so the
// subscriber queue is the only place the fan-out can land: at 64 KiB a frame
// over 8,000 entries an unbounded queue would hold 512 MiB per stream, 16x the
// 32 MiB budget the SSE handler subscribes with.
func BenchmarkServeFanout(b *testing.B) {
	const (
		entries = 8000
		streams = 4
		bound   = 32 << 20 // the serve layer's 2 × maxTailBytes
		size    = 64 << 10
	)
	payload := make([]byte, size)
	b.SetBytes(size)
	bus := newBus(nil)
	for range b.N {
		subs := make([]*Subscription, streams)
		for i := range subs {
			subs[i] = bus.Subscribe(bound)
		}
		dropped := 0
		for range entries {
			for _, sub := range subs {
				if sub.Push("message.part.delta", payload) {
					dropped++
				}
			}
		}
		// The bound is the evidence: an unbounded queue would hold
		// entries × size = 512 MiB per stream, this holds 32 MiB and stops.
		b.ReportMetric(float64(dropped), "drops/op")
		b.ReportMetric(float64(subs[0].queued), "queued-bytes/op")
		for _, sub := range subs {
			sub.Close()
		}
	}
}
