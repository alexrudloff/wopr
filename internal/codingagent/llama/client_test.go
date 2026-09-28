package llama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseHub registers /models/sse responses and broadcasts events to them.
type sseHub struct {
	mu      sync.Mutex
	streams map[http.ResponseWriter]chan string
}

func newSSEHub() *sseHub { return &sseHub{streams: map[http.ResponseWriter]chan string{}} }

func (h *sseHub) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	events := make(chan string, 16)
	h.mu.Lock()
	h.streams[w] = events
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.streams, w)
		h.mu.Unlock()
	}()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-events:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
			w.(http.Flusher).Flush()
		}
	}
}

func (h *sseHub) send(event any) {
	data, _ := json.Marshal(event)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, events := range h.streams {
		events <- string(data)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func listen(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return server.URL
}

func TestLoadAndWaitReportsSSEProgressAndWaitsForLoadedCatalogState(t *testing.T) {
	var mu sync.Mutex
	status := "unloaded"
	hub := newSSEHub()
	url := listen(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/models/sse":
			hub.serve(w, r)
		case r.URL.Path == "/models/load" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "test-model" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("load body = %v, content type %q", body, r.Header.Get("Content-Type"))
			}
			mu.Lock()
			status = "loading"
			mu.Unlock()
			writeJSON(w, map[string]any{"success": true})
			time.AfterFunc(20*time.Millisecond, func() {
				hub.send(map[string]any{"model": "test-model", "event": "status_change", "data": map[string]any{
					"status":   "loading",
					"progress": map[string]any{"stages": []string{"text_model", "mmproj_model"}, "current": "text_model", "value": 0.5},
				}})
				mu.Lock()
				status = "loaded"
				mu.Unlock()
				hub.send(map[string]any{"model": "test-model", "event": "status_change", "data": map[string]any{"status": "loaded"}})
			})
		case r.URL.Path == "/models":
			mu.Lock()
			current := status
			mu.Unlock()
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "test-model", "status": map[string]any{"value": current}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	client, err := NewLlamaClient(url, "")
	if err != nil {
		t.Fatal(err)
	}
	var progress []LlamaProgress
	model, err := client.LoadAndWait(context.Background(), "test-model", func(entry LlamaProgress) { progress = append(progress, entry) })
	if err != nil {
		t.Fatal(err)
	}
	if model.Status.Value != LlamaModelStatusLoaded {
		t.Fatalf("status = %q, want loaded", model.Status.Value)
	}
	index := slices.IndexFunc(progress, func(entry LlamaProgress) bool { return entry.Message == "Loading text model" })
	if index < 0 {
		t.Fatalf("progress = %+v, want Loading text model", progress)
	}
	if ratio := progress[index].Ratio; ratio == nil || *ratio != 0.25 {
		t.Fatalf("stage ratio = %v, want 0.25", ratio)
	}
}

func TestReadEventsSkipsMalformedEvents(t *testing.T) {
	var events []LlamaModelEvent
	body := "\uFEFFdata: {\"model\":\"m\",\"event\":\"e\",\"data\":{\"x\":\"\xc3\xa9\"}}\r\n\r\ndata: nope\n\ndata: {\"model\":1}\n\n"
	if err := readEvents(strings.NewReader(body), func(event LlamaModelEvent) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Model != "m" || events[0].Event != "e" || string(events[0].Data) != `{"x":"é"}` {
		t.Fatalf("events = %+v", events)
	}
}
