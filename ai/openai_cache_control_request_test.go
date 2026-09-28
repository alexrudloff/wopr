package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Anthropic cache control on openai-completions marks the first instruction,
// last tool definition, and last text-bearing conversation message. Golden bodies
// distinguish text-part annotations from message annotations and preserve images.
func TestCompletionsRequestAnthropicCacheControl(t *testing.T) {
	call := ToolCall{ID: "call", Name: "read", Arguments: JsonObject{"path": "x"}}
	for _, tc := range []struct {
		name     string
		messages []Message
		want     string
	}{
		{"user string", []Message{UserMessage{Content: UserText("hello")}}, `[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]`},
		{"tool result", []Message{UserMessage{Content: UserText("read")}, AssistantMessage{Content: []AssistantContentBlock{call}}, ToolResultMessage{ToolCallID: "call", ToolName: "read", Content: []ToolResultMessageContent{TextContent{Text: "result"}}}}, `[{"role":"user","content":"read"},{"role":"assistant","content":"","tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call","content":[{"type":"text","text":"result","cache_control":{"type":"ephemeral"}}]}]`},
		{"skip tool-only assistant", []Message{UserMessage{Content: UserText("read")}, AssistantMessage{Content: []AssistantContentBlock{call}}}, `[{"role":"user","content":[{"type":"text","text":"read","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":"","tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := append([]Message{SystemMessage{Content: SystemText("system"), ToolsAdded: []ToolSchema{
				{Name: "first", Description: "first tool", Parameters: JsonObject{"type": "object"}},
				{Name: "read", Description: "read tool", Parameters: JsonObject{"type": "object"}},
			}}}, tc.messages...)
			body := captureShapeRequest(t, func(url string) Provider {
				return NewOpenAIProvider(OpenAIConfig{BaseURL: url, APIKey: "test", Model: "test", ProviderID: "openrouter", Compat: &ModelCompat{CacheControlFormat: "anthropic", SupportsStore: new(false), SupportsStrictMode: new(false)}})
			}, messages, StreamOptions{Env: ProviderEnv{"WOPR_CACHE_RETENTION": "short"}}, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			got, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			assertShapeJSON(t, got, `{"model":"test","stream":true,"stream_options":{"include_usage":true},
				"messages":[{"role":"system","content":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}]},`+strings.TrimPrefix(tc.want, "[")+`,
				"tools":[{"type":"function","function":{"name":"first","description":"first tool","parameters":{"type":"object"}}},{"type":"function","function":{"name":"read","description":"read tool","parameters":{"type":"object"}},"cache_control":{"type":"ephemeral"}}]}`)
		})
	}
}

func TestCompletionsRequestAnthropicCacheRetention(t *testing.T) {
	for _, tc := range []struct {
		name      string
		format    string
		retention CacheRetention
		env       string
		long      bool
		want      string
	}{
		{"disabled", "anthropic", CacheRetentionNone, "long", true, `"hello"`},
		{"long", "anthropic", CacheRetentionLong, "short", true, `[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := captureShapeRequest(t, func(url string) Provider {
				return NewOpenAIProvider(OpenAIConfig{BaseURL: url, APIKey: "test", Model: "test", ProviderID: "openrouter", Compat: &ModelCompat{CacheControlFormat: tc.format, SupportsLongCacheRetention: new(tc.long)}})
			}, []Message{UserMessage{Content: UserText("hello")}}, StreamOptions{CacheRetention: tc.retention, Env: ProviderEnv{"WOPR_CACHE_RETENTION": tc.env}}, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			assertShapeJSON(t, body["messages"], `[{"role":"user","content":`+tc.want+`}]`)
		})
	}
}

// captureShapeRequest exercises the public provider boundary and the actual JSON encoder.
func captureShapeRequest(t *testing.T, factory func(string) Provider, messages []Message, opts StreamOptions, reply string) map[string]json.RawMessage {
	t.Helper()
	requests := make(chan map[string]json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	provider := factory(server.URL)
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: messages}), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result := stream.Result(); result == nil || result.StopReason != StopReasonStop {
		t.Fatalf("result = %#v", result)
	}
	select {
	case body := <-requests:
		return body
	default:
		t.Fatal("provider completed without an HTTP request")
		return nil
	}
}

func assertShapeJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("request shape\n got: %s\nwant: %s", got, want)
	}
}

// Responses message conversion maps every text block, including
