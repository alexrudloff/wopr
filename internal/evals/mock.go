// Package evals measures wopr: overhead against a deterministic local model,
// fixed coding tasks against real models, seeded mutation tasks, and
// performance budgets. cmd/wopr-eval is its command line.
package evals

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Wire protocols the mock model speaks. They match the api names in
// models.json.
const (
	ProtocolChat      = "openai-completions"
	ProtocolResponses = "openai-responses"
	ProtocolMessages  = "anthropic-messages"
	protocolOther     = "other"
)

// Protocols lists the protocols in report order.
var Protocols = []string{ProtocolChat, ProtocolResponses, ProtocolMessages}

// MockReply is the one reply the mock model gives; MockModel is its model id.
const (
	MockReply = "Hello from the benchmark provider."
	MockModel = "bench-1"
)

// Request is one model request the mock received.
type Request struct {
	Protocol    string
	Path        string
	Bytes       int
	SystemBytes int
	Tools       int
	Stream      bool
}

// MockServer answers every model request with MockReply over the protocol
// the request path names, so a measurement sees the client and not a model.
type MockServer struct {
	URL string

	srv      *http.Server
	mu       sync.Mutex
	requests []Request
}

// StartMock serves the mock on an ephemeral 127.0.0.1 port until Close.
func StartMock() (*MockServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	m := &MockServer{URL: "http://" + ln.Addr().String()}
	m.srv = &http.Server{Handler: m, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = m.srv.Serve(ln) }()
	return m, nil
}

// Close stops the server.
func (m *MockServer) Close() error { return m.srv.Close() }

// Take returns the model requests recorded since the last Take and forgets
// them.
func (m *MockServer) Take() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := m.requests
	m.requests = nil
	return taken
}

func (m *MockServer) record(r Request) {
	m.mu.Lock()
	m.requests = append(m.requests, r)
	m.mu.Unlock()
}

// ProtocolForPath maps a request path to its protocol.
func ProtocolForPath(path string) string {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return ProtocolChat
	case strings.HasSuffix(path, "/responses"):
		return ProtocolResponses
	case strings.HasSuffix(path, "/messages"):
		return ProtocolMessages
	}
	return protocolOther
}

const noRoute = `{"error":{"message":"wopr-eval mock: no route"}}`

func (m *MockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch r.Method {
	case http.MethodHead:
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodGet:
		if strings.HasSuffix(path, "/models") {
			writeJSON(w, map[string]any{"object": "list", "data": []any{map[string]any{"id": MockModel, "object": "model", "created": 0, "owned_by": "wopr-eval"}}})
			return
		}
		http.Error(w, noRoute, http.StatusNotFound)
		return
	case http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var reader io.Reader = r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.HasSuffix(path, "/messages/count_tokens") {
		writeJSON(w, map[string]any{"input_tokens": max(1, len(raw)/4)})
		return
	}
	protocol := ProtocolForPath(path)
	if protocol == protocolOther {
		http.Error(w, noRoute, http.StatusNotFound)
		return
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	tools, _ := body["tools"].([]any)
	stream, _ := body["stream"].(bool)
	m.record(Request{Protocol: protocol, Path: path, Bytes: len(raw), SystemBytes: len(SystemText(protocol, body)), Tools: len(tools), Stream: stream})
	model, _ := body["model"].(string)
	if model == "" {
		model = MockModel
	}
	reply := replyWriter{w: w, model: model, tokensIn: max(1, len(raw)/4), stream: stream}
	switch protocol {
	case ProtocolChat:
		reply.chat()
	case ProtocolResponses:
		reply.responses()
	case ProtocolMessages:
		reply.messages()
	}
}

// SystemText returns the system prompt a request body carries.
func SystemText(protocol string, body map[string]any) string {
	var out strings.Builder
	var list []any
	switch protocol {
	case ProtocolMessages:
		return contentText(body["system"])
	case ProtocolResponses:
		instructions, _ := body["instructions"].(string)
		out.WriteString(instructions)
		list, _ = body["input"].([]any)
	default:
		list, _ = body["messages"].([]any)
	}
	for _, item := range list {
		if msg, ok := item.(map[string]any); ok && (msg["role"] == "system" || msg["role"] == "developer") {
			out.WriteString(contentText(msg["content"]))
		}
	}
	return out.String()
}

func contentText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var out strings.Builder
		for _, part := range c {
			if p, ok := part.(map[string]any); ok {
				if text, ok := p["text"].(string); ok {
					out.WriteString(text)
				}
			}
		}
		return out.String()
	}
	return ""
}

// replyDeltas splits MockReply into streamed word deltas.
func replyDeltas() []string {
	words := strings.Split(MockReply, " ")
	for i := range len(words) - 1 {
		words[i] += " "
	}
	return words
}

type replyWriter struct {
	w        http.ResponseWriter
	model    string
	tokensIn int
	stream   bool
}

func writeJSON(w http.ResponseWriter, payload any) {
	data, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}

func (r replyWriter) begin() {
	r.w.Header().Set("Content-Type", "text/event-stream")
	r.w.Header().Set("Cache-Control", "no-cache")
	r.w.WriteHeader(http.StatusOK)
}

func (r replyWriter) event(name string, data any) {
	payload, _ := json.Marshal(data)
	if name != "" {
		_, _ = fmt.Fprintf(r.w, "event: %s\n", name)
	}
	_, _ = fmt.Fprintf(r.w, "data: %s\n\n", payload)
}

func (r replyWriter) flush() {
	if f, ok := r.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (r replyWriter) chat() {
	parts := replyDeltas()
	usage := map[string]any{"prompt_tokens": r.tokensIn, "completion_tokens": len(parts), "total_tokens": r.tokensIn + len(parts)}
	if !r.stream {
		writeJSON(r.w, map[string]any{"id": "chatcmpl-bench", "object": "chat.completion", "created": 0, "model": r.model, "usage": usage,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": MockReply}, "finish_reason": "stop"}}})
		return
	}
	r.begin()
	chunk := func(extra map[string]any) map[string]any {
		out := map[string]any{"id": "chatcmpl-bench", "object": "chat.completion.chunk", "created": 0, "model": r.model}
		maps.Copy(out, extra)
		return out
	}
	for i, part := range parts {
		delta := map[string]any{"content": part}
		if i == 0 {
			delta["role"] = "assistant"
		}
		r.event("", chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}))
	}
	r.event("", chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}))
	r.event("", chunk(map[string]any{"choices": []any{}, "usage": usage}))
	_, _ = io.WriteString(r.w, "data: [DONE]\n\n")
	r.flush()
}

func (r replyWriter) responses() {
	parts := replyDeltas()
	content := map[string]any{"type": "output_text", "text": MockReply, "annotations": []any{}}
	item := map[string]any{"id": "msg_bench", "type": "message", "status": "completed", "role": "assistant", "content": []any{content}}
	usage := map[string]any{"input_tokens": r.tokensIn, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": len(parts),
		"output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": r.tokensIn + len(parts)}
	response := func(status string, output []any, usage any) map[string]any {
		return map[string]any{"id": "resp_bench", "object": "response", "created_at": 0, "status": status, "model": r.model, "output": output, "usage": usage}
	}
	final := response("completed", []any{item}, usage)
	if !r.stream {
		writeJSON(r.w, final)
		return
	}
	r.begin()
	seq := 0
	emit := func(name string, data map[string]any) {
		data["type"], data["sequence_number"] = name, seq
		seq++
		r.event(name, data)
	}
	pending := map[string]any{"id": "msg_bench", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	emit("response.created", map[string]any{"response": response("in_progress", []any{}, nil)})
	emit("response.in_progress", map[string]any{"response": response("in_progress", []any{}, nil)})
	emit("response.output_item.added", map[string]any{"output_index": 0, "item": pending})
	emit("response.content_part.added", map[string]any{"item_id": "msg_bench", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	for _, part := range parts {
		emit("response.output_text.delta", map[string]any{"item_id": "msg_bench", "output_index": 0, "content_index": 0, "delta": part, "logprobs": []any{}})
	}
	emit("response.output_text.done", map[string]any{"item_id": "msg_bench", "output_index": 0, "content_index": 0, "text": MockReply, "logprobs": []any{}})
	emit("response.content_part.done", map[string]any{"item_id": "msg_bench", "output_index": 0, "content_index": 0, "part": content})
	emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	emit("response.completed", map[string]any{"response": final})
	r.flush()
}

func (r replyWriter) messages() {
	parts := replyDeltas()
	usage := map[string]any{"input_tokens": r.tokensIn, "output_tokens": len(parts), "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
	message := func(content []any, stop, usage any) map[string]any {
		return map[string]any{"id": "msg_bench", "type": "message", "role": "assistant", "model": r.model, "stop_reason": stop, "stop_sequence": nil, "usage": usage, "content": content}
	}
	if !r.stream {
		writeJSON(r.w, message([]any{map[string]any{"type": "text", "text": MockReply}}, "end_turn", usage))
		return
	}
	r.begin()
	startUsage := map[string]any{"input_tokens": r.tokensIn, "output_tokens": 1, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
	r.event("message_start", map[string]any{"type": "message_start", "message": message([]any{}, nil, startUsage)})
	r.event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	for _, part := range parts {
		r.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": part}})
	}
	r.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	r.event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": len(parts)}})
	r.event("message_stop", map[string]any{"type": "message_stop"})
	r.flush()
}
