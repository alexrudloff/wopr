package ai

// Codex transport regressions. All servers are loopback httptest servers; no
// real network is used.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func reviewCodexProvider(t *testing.T, url, account string) Provider {
	t.Helper()
	return NewOpenAICodexResponsesProvider(OpenAICodexResponsesConfig{
		APIKey:     codexTestToken(t, account),
		Model:      "gpt-5.2",
		ProviderID: "openai-codex",
		BaseURL:    url,
	})
}

func reviewCodexCtx(text string) TranscriptContext {
	return NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText(text)}}})
}

const reviewSSEDone = "data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_sse\",\"status\":\"completed\"}}\n\n"

// Every transport failure, including one after the message stream started,
// records a WebSocket failure for the session, so the next request in that
// session goes straight to SSE.
func TestCodexMidStreamWebSocketFailureMakesSessionFallBackToSSE(t *testing.T) {
	var wsAttempts, sseAttempts atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sseAttempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(reviewSSEDone))
			return
		}
		wsAttempts.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			_ = conn.Close()
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "msg_x", "status": "in_progress"}})
		_ = conn.WriteJSON(map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "partial"})
		_ = conn.Close() // abrupt transport drop after output started
	}))
	defer server.Close()
	defer CloseOpenAICodexWebSocketSessions()
	resetCodexWebSocketFallback()

	provider := reviewCodexProvider(t, server.URL, "acct_mid")
	options := StreamOptions{Transport: TransportAuto, SessionID: "review-mid", TimeoutMs: 1000}
	first, err := provider.Stream(context.Background(), reviewCodexCtx("one"), options)
	if err != nil {
		t.Fatal(err)
	}
	if r := first.Result(); r.StopReason != StopReasonError {
		t.Fatalf("first stop=%q, want error after mid-stream drop", r.StopReason)
	}
	second, err := provider.Stream(context.Background(), reviewCodexCtx("two"), options)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Result()
	if wsAttempts.Load() != 1 || sseAttempts.Load() != 1 {
		t.Errorf("attempts ws=%d sse=%d, want ws=1 sse=1 (session fallback is sticky after a WebSocket failure)", wsAttempts.Load(), sseAttempts.Load())
	}
	if !codexWebSocketFallbackActive("review-mid") {
		t.Error("WebSocket fallback is not active after the failure")
	}
}

func TestCodexSSEFramingAndErrorMapping(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantStop  StopReason
		wantError string
	}{
		{
			name:     "data field needs no space and may span lines",
			body:     "data:{\"type\":\ndata: \"response.done\",\"response\":{\"id\":\"resp_multiline\",\"status\":\"completed\"}}\n\n",
			wantStop: StopReasonStop,
		},
		{
			name:      "nested Codex error",
			body:      "data: {\"type\":\"error\",\"error\":{\"code\":\"bad\",\"message\":\"broken\"}}\n\n",
			wantStop:  StopReasonError,
			wantError: "Codex error: broken",
		},
		{
			name:      "response failed without details",
			body:      "data: {\"type\":\"response.failed\",\"response\":{}}\n\n",
			wantStop:  StopReasonError,
			wantError: "Codex response failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			stream, err := reviewCodexProvider(t, server.URL, "acct_frames").Stream(context.Background(), reviewCodexCtx("hi"), StreamOptions{Transport: TransportSSE})
			if err != nil {
				t.Fatal(err)
			}
			result := stream.Result()
			if result.StopReason != test.wantStop {
				t.Fatalf("stop=%q error=%q, want %q", result.StopReason, result.ErrorMessage, test.wantStop)
			}
			if test.wantError != "" && !strings.Contains(result.ErrorMessage, test.wantError) {
				t.Errorf("error=%q, want %q", result.ErrorMessage, test.wantError)
			}
		})
	}
}

// The SSE request body is zstd-encoded.
func TestCodexSSEBodyIsZstdCompressed(t *testing.T) {
	var encoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(reviewSSEDone))
	}))
	defer server.Close()
	stream, err := reviewCodexProvider(t, server.URL, "acct_zstd").Stream(context.Background(), reviewCodexCtx("hi"), StreamOptions{Transport: TransportSSE})
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Result()
	if encoding != "zstd" {
		t.Errorf("Content-Encoding=%q, want zstd", encoding)
	}
}
