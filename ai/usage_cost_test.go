package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Expected usage values are reference serialized AssistantMessage.usage for
// these exact response bodies with the same catalog model.
func TestProviderUsageCost(t *testing.T) {
	for _, tc := range []struct {
		name, kind, spec, body, want, responseModel string
	}{
		{
			name: "anthropic 1h cache write at twice input", kind: "anthropic", spec: "anthropic/claude-opus-4-8",
			body: `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","usage":{"input_tokens":100,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":1000000,"cache_creation":{"ephemeral_5m_input_tokens":600000,"ephemeral_1h_input_tokens":400000}}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":1000000}}

event: message_stop
data: {"type":"message_stop"}

`,
			want: `{"input":100,"output":5,"cacheRead":0,"cacheWrite":1000000,"totalTokens":1000105,"cost":{"input":0.0005,"output":0.000125,"cacheRead":0,"cacheWrite":7.75,"total":7.750625},"cacheWrite1h":400000}`,
		},
		{
			name: "completions prompt_cache_hit_tokens", kind: "completions", spec: "deepseek/deepseek-v4-pro",
			body: `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12345,"completion_tokens":678,"prompt_cache_hit_tokens":10000}}

data: [DONE]

`,
			want: `{"input":2345,"output":678,"cacheRead":10000,"cacheWrite":0,"reasoning":0,"totalTokens":13023,"cost":{"input":0.0030954000000000003,"output":0.00268488,"cacheRead":0.00043999999999999996,"cacheWrite":0,"total":0.006220280000000001}}`,
		},
		{
			name: "anthropic input_tokens excludes cache reads", kind: "anthropic", spec: "anthropic/claude-opus-4-8",
			body: `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","usage":{"input_tokens":200,"output_tokens":0,"cache_read_input_tokens":10000,"cache_creation_input_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}

event: message_stop
data: {"type":"message_stop"}

`,
			want: `{"input":200,"output":50,"cacheRead":10000,"cacheWrite":0,"cacheWrite1h":0,"totalTokens":10250,"cost":{"input":0.001,"output":0.00125,"cacheRead":0.005,"cacheWrite":0,"total":0.00725}}`,
		},
		{
			name: "completions prompt_tokens includes cached_tokens", kind: "completions", spec: "deepseek/deepseek-v4-pro",
			body: `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12345,"completion_tokens":678,"prompt_tokens_details":{"cached_tokens":10000}}}

data: [DONE]

`,
			want: `{"input":2345,"output":678,"cacheRead":10000,"cacheWrite":0,"reasoning":0,"totalTokens":13023,"cost":{"input":0.0030954000000000003,"output":0.00268488,"cacheRead":0.00043999999999999996,"cacheWrite":0,"total":0.006220280000000001}}`,
		},
		{
			name: "responses input_tokens includes cached_tokens", kind: "codex", spec: "openai-codex/gpt-5.5",
			body: `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":100000,"output_tokens":1000,"total_tokens":101000,"input_tokens_details":{"cached_tokens":80000}}}}

`,
			want: `{"input":20000,"output":1000,"cacheRead":80000,"cacheWrite":0,"reasoning":0,"totalTokens":101000,"cost":{"input":0.1,"output":0.030000000000000002,"cacheRead":0.04,"cacheWrite":0,"total":0.17}}`,
		},
		{
			name: "codex gpt-5.5 priority", kind: "codex", spec: "openai-codex/gpt-5.5",
			body: `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","service_tier":"priority","usage":{"input_tokens":100000,"output_tokens":100000,"total_tokens":200000,"input_tokens_details":{"cached_tokens":0}}}}

`,
			want: `{"input":100000,"output":100000,"cacheRead":0,"cacheWrite":0,"reasoning":0,"totalTokens":200000,"cost":{"input":1.25,"output":7.5,"cacheRead":0,"cacheWrite":0,"total":8.75}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generated, ok := LookupModelExact(tc.spec)
			if !ok {
				t.Fatalf("catalog model %s missing", tc.spec)
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, tc.body)
			}))
			t.Cleanup(server.Close)
			provider := usageCostProvider(tc.kind, strings.SplitN(tc.spec, "/", 2), server.URL)
			transcript := NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}})
			stream, err := provider.Stream(context.Background(), transcript, StreamOptions{ModelCost: (&Model{Capabilities: generated.ToCapabilities()}).CostRates()})
			if err != nil {
				t.Fatal(err)
			}
			result := stream.Result()
			if result.StopReason == StopReasonError {
				t.Fatalf("stream error: %s", result.ErrorMessage)
			}
			assertUsageJSON(t, result.Usage, tc.want)
			if result.ResponseModel != tc.responseModel {
				t.Fatalf("responseModel = %q, want %q", result.ResponseModel, tc.responseModel)
			}
		})
	}
}

func usageCostProvider(kind string, spec []string, baseURL string) Provider {
	providerID, modelID := spec[0], spec[1]
	switch kind {
	case "anthropic":
		return NewAnthropicProvider(AnthropicConfig{APIKey: "k", Model: modelID, BaseURL: baseURL, ProviderID: providerID})
	case "completions":
		return NewOpenAIProvider(OpenAIConfig{APIKey: "k", Model: modelID, BaseURL: baseURL, ProviderID: providerID})
	case "codex":
		return NewOpenAICodexResponsesProvider(OpenAICodexResponsesConfig{APIKey: codexTestTokenForAccount("usage-cost"), Model: modelID, BaseURL: baseURL, ProviderID: providerID})
	}
	panic("unknown provider kind " + kind)
}

func assertUsageJSON(t *testing.T, usage Usage, want string) {
	t.Helper()
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("usage = %s\nwant  %s", encoded, want)
	}
}
