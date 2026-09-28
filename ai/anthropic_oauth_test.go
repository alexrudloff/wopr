package ai

// Serialized-request tests for Anthropic auth and tool-name normalization. A
// local server echoes the Claude Code tool name the model would return.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const anthropicEndTurnSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","usage":{"input_tokens":1,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`

type anthropicWireCapture struct {
	header http.Header
	body   map[string]any
}

func anthropicToolUseSSE(name string) string {
	return fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_tool","usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/test.txt\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`, name)
}

// clearAnthropicAuthEnv isolates a test from a developer's Anthropic env.
func clearAnthropicAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "WOPR_CACHE_RETENTION"} {
		t.Setenv(name, "")
	}
}

// runAnthropicWire streams one request through a local server that answers
// with sse (or the SSE its responder returns for the request body) and returns
// the captured request and the emitted events.
func runAnthropicWire(t *testing.T, cfg AnthropicConfig, context Context, opts StreamOptions, respond func(body map[string]any) string) (anthropicWireCapture, []AssistantMessageEvent) {
	t.Helper()
	var captured anthropicWireCapture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.header = r.Header.Clone()
		if err := json.Unmarshal(raw, &captured.body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, respond(captured.body))
	}))
	t.Cleanup(server.Close)
	cfg.BaseURL = server.URL
	if cfg.Model == "" {
		cfg.Model = "claude-test"
	}
	stream, err := NewAnthropicProvider(cfg).Stream(t.Context(), NormalizeContext(context), opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var events []AssistantMessageEvent
	for event := range stream.Events(t.Context()) {
		events = append(events, event)
	}
	return captured, events
}

func endTurn(map[string]any) string { return anthropicEndTurnSSE }

var anthropicAuthContext = Context{
	SystemPrompt: "System prompt.",
	Messages:     []Message{UserMessage{Content: UserText("Hello")}},
	Tools: []ToolSchema{
		{Name: "read", Description: "Read a file", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		{Name: "todowrite", Description: "Write a todo item", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
	},
}

func requestToolNames(body map[string]any) []string {
	var names []string
	tools, _ := body["tools"].([]any)
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	return names
}

func systemTexts(body map[string]any) []string {
	var texts []string
	blocks, _ := body["system"].([]any)
	for _, block := range blocks {
		block := block.(map[string]any)
		if cc, _ := block["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
			texts = append(texts, "missing cache_control: "+block["text"].(string))
			continue
		}
		texts = append(texts, block["text"].(string))
	}
	return texts
}

// TestAnthropicRequestAuthShapes serializes one request per auth source and
// checks the auth headers, Claude Code identity headers, betas, system blocks,
// and tool names createClient and buildParams produce.
func TestAnthropicRequestAuthShapes(t *testing.T) {
	oauthBetas := "claude-code-20250219,oauth-2025-04-20"
	for _, tc := range []struct {
		name          string
		cfg           AnthropicConfig
		env           map[string]string
		headers       ProviderHeaders
		apiKey        string
		authorization string
		userAgent     string
		xApp          string
		beta          string
		system        []string
		tools         []string
	}{
		{
			name:   "api key",
			cfg:    AnthropicConfig{APIKey: "sk-ant-api03-test"},
			apiKey: "sk-ant-api03-test", system: []string{"System prompt."}, tools: []string{"read", "todowrite"},
		},
		{
			name:          "oauth token",
			cfg:           AnthropicConfig{APIKey: "sk-ant-oat01-test"},
			authorization: "Bearer sk-ant-oat01-test", userAgent: "claude-cli/2.1.280", xApp: "cli", beta: oauthBetas,
			system: []string{claudeCodeSystemPrompt, "System prompt."}, tools: []string{"Read", "TodoWrite"},
		},
		{
			// Resolves ANTHROPIC_AUTH_TOKEN as a bearer Authorization header.
			name:          "ANTHROPIC_AUTH_TOKEN",
			env:           map[string]string{"ANTHROPIC_AUTH_TOKEN": "auth-token"},
			authorization: "Bearer auth-token", system: []string{"System prompt."}, tools: []string{"read", "todowrite"},
		},
		{
			// A stored or configured key owns the provider; the ambient token is not consulted.
			name:   "configured key outranks ANTHROPIC_AUTH_TOKEN",
			cfg:    AnthropicConfig{APIKey: "sk-ant-api03-test"},
			env:    map[string]string{"ANTHROPIC_AUTH_TOKEN": "auth-token"},
			apiKey: "sk-ant-api03-test", system: []string{"System prompt."}, tools: []string{"read", "todowrite"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearAnthropicAuthEnv(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			captured, events := runAnthropicWire(t, tc.cfg, anthropicAuthContext, StreamOptions{Headers: tc.headers}, endTurn)
			if message := anthropicTerminal(t, events); message.StopReason != StopReasonStop {
				t.Fatalf("stop reason = %q (%s)", message.StopReason, message.ErrorMessage)
			}
			header := captured.header
			for name, want := range map[string]string{
				"X-Api-Key":      tc.apiKey,
				"Authorization":  tc.authorization,
				"X-App":          tc.xApp,
				"Anthropic-Beta": tc.beta,
				"Accept":         "application/json",
				"Anthropic-Dangerous-Direct-Browser-Access": "true",
				"Anthropic-Version":                         "2023-06-01",
			} {
				if got := header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if _, present := header["Anthropic-Beta"]; present != (tc.beta != "") {
				t.Errorf("anthropic-beta present = %v, want %v", present, tc.beta != "")
			}
			userAgent := header.Get("User-Agent")
			if tc.userAgent != "" && userAgent != tc.userAgent {
				t.Errorf("User-Agent = %q, want %q", userAgent, tc.userAgent)
			}
			if tc.userAgent == "" && strings.HasPrefix(userAgent, "claude-cli/") {
				t.Errorf("User-Agent = %q, want no Claude Code identity", userAgent)
			}
			if _, present := captured.body["betas"]; present {
				t.Errorf("request body carries betas: %v", captured.body["betas"])
			}
			if got := systemTexts(captured.body); !reflect.DeepEqual(got, tc.system) {
				t.Errorf("system = %q, want %q", got, tc.system)
			}
			if got := requestToolNames(captured.body); !reflect.DeepEqual(got, tc.tools) {
				t.Errorf("tool names = %q, want %q", got, tc.tools)
			}
		})
	}
}

// TestAnthropicOAuthToolNameRoundTrip covers tool-name normalization: an
// OAuth request sends
// Claude Code casing, the model answers with that name, and every streamed
// tool-call event and the final message carry the original tool name.
func TestAnthropicOAuthToolNameRoundTrip(t *testing.T) {
	for _, tc := range []struct{ tool, outbound string }{
		{"todowrite", "TodoWrite"},
		{"read", "Read"},
		{"find", "find"},
		{"my_custom_tool", "my_custom_tool"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			clearAnthropicAuthEnv(t)
			context := Context{
				SystemPrompt: "You are a helpful assistant.",
				Messages:     []Message{UserMessage{Content: UserText("Use the " + tc.tool + " tool.")}},
				Tools:        []ToolSchema{{Name: tc.tool, Description: "tool", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
			}
			captured, events := runAnthropicWire(t, AnthropicConfig{APIKey: "sk-ant-oat-test"}, context, StreamOptions{}, func(body map[string]any) string {
				return anthropicToolUseSSE(requestToolNames(body)[0])
			})
			if got := requestToolNames(captured.body); !reflect.DeepEqual(got, []string{tc.outbound}) {
				t.Fatalf("outbound tool names = %q, want [%q]", got, tc.outbound)
			}
			var toolEvents int
			for _, event := range events {
				var partial *AssistantMessage
				switch event := event.(type) {
				case ToolCallStartEvent:
					partial = event.Partial
				case ToolCallDeltaEvent:
					partial = event.Partial
				case ToolCallEndEvent:
					if event.ToolCall.Name != tc.tool {
						t.Errorf("toolcall_end name = %q, want %q", event.ToolCall.Name, tc.tool)
					}
					partial = event.Partial
				default:
					continue
				}
				toolEvents++
				if call, ok := partial.Content[0].(ToolCall); !ok || call.Name != tc.tool {
					t.Errorf("%s partial tool call = %#v, want name %q", event.EventType(), partial.Content[0], tc.tool)
				}
			}
			if toolEvents != 3 {
				t.Errorf("tool-call events = %d, want start, delta, and end", toolEvents)
			}
			message := anthropicTerminal(t, events)
			if message.StopReason != StopReasonToolUse {
				t.Fatalf("stop reason = %q (%s), want toolUse", message.StopReason, message.ErrorMessage)
			}
			if call, ok := message.Content[0].(ToolCall); !ok || call.Name != tc.tool {
				t.Errorf("final tool call = %#v, want name %q", message.Content[0], tc.tool)
			}
		})
	}
}
