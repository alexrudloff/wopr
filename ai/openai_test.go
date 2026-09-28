package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type openAITestRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f openAITestRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDetectCompat_Ollama(t *testing.T) {
	c := DetectCompat("ollama", "http://localhost:11434/v1")
	if c == nil {
		t.Fatal("expected non-nil compat for ollama")
		return
	}
	if *c.SupportsDeveloperRole {
		t.Error("ollama should not support developer role")
	}
	if *c.SupportsReasoningEffort {
		t.Error("ollama should not support reasoning effort")
	}
	if c.MaxTokensField != "max_tokens" {
		t.Errorf("ollama maxTokensField = %q, want max_tokens", c.MaxTokensField)
	}
}

func TestDetectCompat_OpenRouter(t *testing.T) {
	c := DetectCompat("openrouter", "https://openrouter.ai/api/v1")
	if c == nil {
		t.Fatal("expected non-nil compat for openrouter")
		return
	}
	if !*c.SupportsDeveloperRole {
		t.Error("openrouter should support developer role")
	}
	if c.ThinkingFormat != "openrouter" {
		t.Errorf("openrouter thinkingFormat = %q, want openrouter", c.ThinkingFormat)
	}
	if c.MaxTokensField != "max_completion_tokens" {
		t.Errorf("openrouter maxTokensField = %q, want max_completion_tokens", c.MaxTokensField)
	}
}

// parseChunkUsage: prompt_tokens_details.cached_tokens wins over
// prompt_cache_hit_tokens, and cache writes are subtracted from input
// without being subtracted from the cache reads.
func TestParseChunkUsage_PromptTokensDetailsPreferred(t *testing.T) {
	u := parseChunkUsage(&oaiUsage{
		PromptTokens:         100,
		CompletionTokens:     25,
		PromptCacheHitTokens: new(60),
		PromptTokensDetails: &struct {
			CachedTokens     *int `json:"cached_tokens,omitempty"`
			CacheWriteTokens int  `json:"cache_write_tokens,omitempty"`
		}{CachedTokens: new(20), CacheWriteTokens: 7},
		CompletionTokensDetails: &struct {
			ReasoningTokens int `json:"reasoning_tokens,omitempty"`
		}{ReasoningTokens: 9},
	}, ModelCost{})
	if u.Input != 73 || u.CacheRead != 20 || u.CacheWrite != 7 || u.Reasoning == nil || *u.Reasoning != 9 || u.TotalTokens != 125 {
		t.Fatalf("unexpected usage: %+v", u)
	}
}

func TestParseSSE_MissingFinishReasonIsError(t *testing.T) {
	message := runOpenAICompletionsSSE(t, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n")
	if message.StopReason != StopReasonError || !strings.Contains(message.ErrorMessage, "finish_reason") {
		t.Fatalf("message = %#v", message)
	}
}

// captureThinkingPayload drives a completions Stream with the given compat +
// thinking level and returns the decoded request body. Oracle for the
// thinkingFormat switch.
func captureThinkingPayload(t *testing.T, providerID, baseURL string, compat *ModelCompat, level ThinkingLevel) oaiRequest {
	t.Helper()
	var reqBody oaiRequest
	p := &openAIProvider{cfg: OpenAIConfig{BaseURL: baseURL, Model: "m", ProviderID: providerID, Compat: compat}}
	p.client = &http.Client{Transport: openAITestRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&reqBody); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")),
		}, nil
	})}
	stream, err := p.Stream(context.Background(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{IsReasoning: true, Thinking: level})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	for range stream.Events(context.Background()) {
	}
	return reqBody
}

func TestStreamOpenRouterThinkingPayload(t *testing.T) {
	compat := &ModelCompat{ThinkingFormat: "openrouter", MaxTokensField: "max_tokens"}
	req := captureThinkingPayload(t, "openrouter", "https://openrouter.ai/api/v1", compat, ThinkingHigh)
	r, ok := req.Reasoning.(map[string]any)
	if !ok || r["effort"] != "high" {
		t.Fatalf("openrouter reasoning = %#v, want {effort:high}", req.Reasoning)
	}
}

func TestMaxTokensField_Ollama(t *testing.T) {
	// Compat detection has no Ollama case, so a local server gets
	// max_completion_tokens unless models.json sets compat.maxTokensField.
	p := &openAIProvider{cfg: OpenAIConfig{BaseURL: "http://localhost:11434/v1"}}
	if f := p.maxTokensField(); f != "max_completion_tokens" {
		t.Errorf("Ollama maxTokensField = %q, want max_completion_tokens", f)
	}
	p.cfg.Compat = &ModelCompat{MaxTokensField: "max_tokens"}
	if f := p.maxTokensField(); f != "max_tokens" {
		t.Errorf("compat override = %q, want max_tokens", f)
	}
}

func drainParseSSE(t *testing.T, sse string) []AssistantMessageEvent {
	t.Helper()
	provider := &openAIProvider{}
	builder := newAssistantStreamBuilder(context.Background(), APIOpenAICompletions, "openai", "model", ModelCost{})
	provider.parseSSE(context.Background(), strings.NewReader(sse), builder, nil)
	var events []AssistantMessageEvent
	for event := range builder.stream.Events(context.Background()) {
		events = append(events, event)
	}
	return events
}

func sseTextAndDone(t *testing.T, events []AssistantMessageEvent) (string, *AssistantMessage) {
	t.Helper()
	var text strings.Builder
	var done *AssistantMessage
	for _, event := range events {
		switch event := event.(type) {
		case TextDeltaEvent:
			text.WriteString(event.Delta)
		case DoneEvent:
			done = event.Message
		case ErrorEvent:
			t.Fatalf("unexpected error event: %s", event.Error.ErrorMessage)
		}
	}
	return text.String(), done
}

func TestParseSSE_UsageInlineWithContentDoesNotAbort(t *testing.T) {
	// gemini-3.5-flash via github-copilot attaches a usage object to
	// content-bearing chunks. The parser must record usage and keep
	// processing; returning on the first usage chunk drops the whole stream
	// (the empty-output bug behind empty advisory reviews).
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"VER\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"DICT\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n"

	text, done := sseTextAndDone(t, drainParseSSE(t, sse))
	if text != "VERDICT" {
		t.Fatalf("text = %q, want \"VERDICT\" (content dropped by the usage-abort bug)", text)
	}
	if done == nil {
		t.Fatal("no terminal EventDone emitted")
	}
	if done.StopReason != StopReasonStop {
		t.Errorf("stop reason = %q, want stop", done.StopReason)
	}
	if done.Usage.Output != 3 {
		t.Errorf("usage = %+v, want Output=3 carried to the terminal event", done.Usage)
	}
}

func TestParseSSE_FinishWithUsageNoDoneEmitsTerminal(t *testing.T) {
	// Some providers (e.g. together) close the connection after a final chunk
	// carrying finish_reason + usage, with no [DONE]. The stop reason and
	// usage must still surface on a terminal EventDone via the stream-end path.
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":4}}\n\n"

	text, done := sseTextAndDone(t, drainParseSSE(t, sse))
	if text != "hi" {
		t.Errorf("text = %q, want \"hi\"", text)
	}
	if done == nil {
		t.Fatal("no terminal EventDone on stream end without [DONE]")
	}
	if done.StopReason != StopReasonStop {
		t.Errorf("stop reason = %q, want stop", done.StopReason)
	}
	if done.Usage.Output != 4 {
		t.Errorf("usage = %+v, want Output=4", done.Usage)
	}
}

// TestConvertMessages_AssistantTextIsStringNotArray pins the OpenAI Chat
// Completions request shape for an assistant turn carrying text plus a tool call,
// followed by a tool result. Two invariants:
//
//   - assistant content is a plain string, not a [{type:"text",text}] array
//     (an array makes some models echo the structure literally, e.g. DeepSeek
//     V3.2 via NVIDIA NIM);
//   - request tool_calls carry only id, type, function; no "index" field
//     (index belongs to streaming deltas).
func TestConvertMessages_AssistantTextIsStringNotArray(t *testing.T) {
	messages := []Message{
		UserMessage{Content: UserContentBlocks{TextContent{Text: "look up the weather"}}},
		AssistantMessage{Content: []AssistantContentBlock{
			TextContent{Text: "I'll look it up"},
			ToolCall{ID: "call_1", Name: "lookup", Arguments: JsonObject{"q": "weather"}},
		}},
		ToolResultMessage{ToolCallID: "call_1", Content: []ToolResultMessageContent{TextContent{Text: "sunny and 72F"}}},
	}
	out, cmErr := convertMessages(messages, false, nil)
	if cmErr != nil {
		t.Fatalf("convertMessages: %v", cmErr)
	}

	var assistant *oaiMessage
	for i := range out {
		if out[i].Role == "assistant" {
			assistant = &out[i]
		}
	}
	if assistant == nil {
		t.Fatal("no assistant message produced")
	}
	if got, ok := assistant.Content.(string); !ok || got != "I'll look it up" {
		t.Errorf("assistant content = %#v, want string %q", assistant.Content, "I'll look it up")
	}

	raw, err := json.Marshal(assistant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"index"`) {
		t.Errorf("request tool_calls must not carry an index field: %s", raw)
	}
	if !strings.Contains(string(raw), `"content":"I'll look it up"`) {
		t.Errorf("assistant content must serialize as a string, got: %s", raw)
	}

	// The tool result must lower to a role:tool message keyed by tool_call_id.
	var toolMsg *oaiMessage
	for i := range out {
		if out[i].Role == "tool" {
			toolMsg = &out[i]
		}
	}
	if toolMsg == nil || toolMsg.ToolCallID != "call_1" || toolMsg.Content != "sunny and 72F" {
		t.Errorf("tool result message = %#v, want role:tool content=%q tool_call_id=call_1", toolMsg, "sunny and 72F")
	}
}

// TestOpenAICompletionsEmptyTools covers empty-tools handling. The request `tools`
// field is omitted when there are no active tools and no tool history, but must
// be an explicit empty array once the conversation carries tool history so that
// Anthropic-via-proxy (LiteLLM) backends accept the request.
func TestOpenAICompletionsEmptyTools(t *testing.T) {
	p := &openAIProvider{cfg: OpenAIConfig{Model: "gpt-4o-mini", BaseURL: "https://api.openai.com/v1"}}
	user := UserMessage{Content: UserText("hi")}
	toolHistory := []Message{
		user,
		AssistantMessage{Content: []AssistantContentBlock{ToolCall{ID: "t1", Name: "noop", Arguments: JsonObject{}}}},
		ToolResultMessage{ToolCallID: "t1", Content: []ToolResultMessageContent{TextContent{Text: "done"}}},
	}
	oneTool := []ToolSchema{{Name: "noop", Description: "d", Parameters: map[string]any{"type": "object"}}}

	cases := []struct {
		name      string
		messages  []Message
		tools     []ToolSchema
		wantKey   bool
		wantEmpty bool
	}{
		{"empty tools no history omits", []Message{user}, []ToolSchema{}, false, false},
		{"nil tools no history omits", []Message{user}, nil, false, false},
		{"empty tools with history emits empty array", toolHistory, []ToolSchema{}, true, true},
		{"active tools emit populated array", []Message{user}, oneTool, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := oaiRequest{Model: p.cfg.Model, Messages: []oaiMessage{{Role: "user", Content: "hi"}}, Stream: true}
			tools, terr := p.resolveRequestTools(tc.messages, tc.tools)
			if terr != nil {
				t.Fatalf("resolveRequestTools: %v", terr)
			}
			req.Tools = tools
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			raw, present := wire["tools"]
			if present != tc.wantKey {
				t.Fatalf("tools present = %v, want %v (body=%s)", present, tc.wantKey, body)
			}
			if !present {
				return
			}
			if tc.wantEmpty && string(raw) != "[]" {
				t.Fatalf("tools = %s, want []", raw)
			}
			if !tc.wantEmpty && string(raw) == "[]" {
				t.Fatalf("tools = [], want a populated array")
			}
		})
	}
}

// TestNormalizeCompletionsToolCallID: OpenAI-Responses pipe-format ids
// ({call_id}|{item_id}, up to 400+ chars) must be normalized to Chat
// Completions safe ids so proxies do not reject them as "call_id too long".
// Golden values were produced by an independent shortHash implementation.
func TestNormalizeCompletionsToolCallID(t *testing.T) {
	// The exact failing id from issue #1022.
	fail := "call_pAYbIr76hXIjncD9UE4eGfnS|t5nnb2qYMFWGSsr13fhCd1CaCu3t3qONEPuOudu4HSVEtA8YJSL6FAZUxvoOoD792VIJWl91g87EdqsCWp9krVsdBysQoDaf9lMCLb8BS4EYi4gQd5kBQBYLlgD71PYwvf+TbMD9J9/5OMD42oxSRj8H+vRf78/l2Xla33LWz4nOgsddBlbvabICRs8GHt5C9PK5keFtzyi3lsyVKNlfduK3iphsZqs4MLv4zyGJnvZo/+QzShyk5xnMSQX/f98+aEoNflEApCdEOXipipgeiNWnpFSHbcwmMkZoJhURNu+JEz3xCh1mrXeYoN5o+trLL3IXJacSsLYXDrYTipZZbJFRPAucgbnjYBC+/ZzJOfkwCs+Gkw7EoZR7ZQgJ8ma+9586n4tT4cI8DEhBSZsWMjrCt8dxKg=="
	cases := []struct {
		name, id, provider, want string
	}{
		{"short pipe combines parts", "call_abc|item_xyz", "github-copilot", "call_abc_item_xyz"},
		{"long pipe falls back to hash", fail, "github-copilot", "call_pAYbIr76hXIjncD9UE4eGfnS_1q6evuz1"},
		{"special chars sanitized", "call_a+b/c|d=e/f", "github-copilot", "call_a_b_c_d_e_f"},
		{"openai truncates long non-pipe id", strings.Repeat("x", 45), "openai", strings.Repeat("x", 40)},
		{"non-openai passes long id through", strings.Repeat("y", 50), "anthropic", strings.Repeat("y", 50)},
		{"short id unchanged", "call_123", "openai", "call_123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCompletionsToolCallID(tc.id, tc.provider); got != tc.want {
				t.Fatalf("normalizeCompletionsToolCallID(%.24q, %q) = %q, want %q", tc.id, tc.provider, got, tc.want)
			}
			if got := normalizeCompletionsToolCallID(tc.id, tc.provider); len(got) > 40 && strings.Contains(tc.id, "|") {
				t.Fatalf("pipe id normalized to %d chars, exceeds 40-char limit", len(got))
			}
		})
	}
}

// TestConvertMessagesNormalizesToolCallIDsConsistently drives the production
// convertMessagesWithCompat path and proves an assistant tool call and its
// matching tool result keep the same normalized id, so the request stays valid.
func TestConvertMessagesNormalizesToolCallIDsConsistently(t *testing.T) {
	p := &openAIProvider{cfg: OpenAIConfig{ProviderID: "github-copilot", Model: "gpt-5.2-codex"}}
	pipeID := "call_abc|item_xyz"
	messages := []Message{
		UserMessage{Content: UserText("hi")},
		AssistantMessage{Content: []AssistantContentBlock{ToolCall{ID: pipeID, Name: "echo", Arguments: JsonObject{"message": "hi"}}}},
		ToolResultMessage{ToolCallID: pipeID, Content: []ToolResultMessageContent{TextContent{Text: "hi"}}},
	}
	out, cmErr := p.convertMessagesWithCompat(messages, nil, "system", false)
	if cmErr != nil {
		t.Fatalf("convertMessages: %v", cmErr)
	}

	var callID, resultID string
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			callID = tc.ID
		}
		if m.ToolCallID != "" {
			resultID = m.ToolCallID
		}
	}
	if callID == "" || resultID == "" {
		t.Fatalf("missing tool call/result id: call=%q result=%q", callID, resultID)
	}
	if callID != "call_abc_item_xyz" {
		t.Fatalf("tool call id = %q, want normalized call_abc_item_xyz", callID)
	}
	if callID != resultID {
		t.Fatalf("tool call id %q and tool result id %q diverged; provider would reject the request", callID, resultID)
	}
	if strings.Contains(callID, "|") {
		t.Fatalf("normalized id still contains a pipe: %q", callID)
	}
}

func runOpenAICompletionsSSEForProvider(t *testing.T, providerID, sse string) *AssistantMessage {
	t.Helper()
	provider := &openAIProvider{cfg: OpenAIConfig{ProviderID: providerID}}
	builder := newAssistantStreamBuilder(context.Background(), APIOpenAICompletions, providerID, "model", ModelCost{})
	provider.parseSSE(context.Background(), strings.NewReader(sse), builder, nil)
	return builder.stream.Result()
}

func runOpenAICompletionsSSE(t *testing.T, sse string) *AssistantMessage {
	t.Helper()
	return runOpenAICompletionsSSEForProvider(t, "openai", sse)
}

func TestParseSSE_ToolCallsWithoutIndexRemainDistinct(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"id\":\"a\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"p\\\":1}\"}}," +
		"{\"id\":\"b\",\"type\":\"function\",\"function\":{\"name\":\"ls\",\"arguments\":\"{}\"}}" +
		"]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	message := runOpenAICompletionsSSE(t, sse)
	if len(message.Content) != 2 {
		t.Fatalf("content = %#v, want two tool calls", message.Content)
	}
	first, firstOK := message.Content[0].(ToolCall)
	second, secondOK := message.Content[1].(ToolCall)
	if !firstOK || !secondOK {
		t.Fatalf("content = %#v, want two ToolCall blocks", message.Content)
	}
	if first.ID != "a" || first.Name != "read" || first.Arguments["p"] != float64(1) {
		t.Fatalf("first tool call = %#v", first)
	}
	if second.ID != "b" || second.Name != "ls" || len(second.Arguments) != 0 {
		t.Fatalf("second tool call = %#v", second)
	}
}

// The local/cluster tiers: an OpenAI-compatible server on a non-OpenAI base
// URL with no API key and Ollama-style compat, streaming text plus a tool call.
func TestOpenAICompletionsLocalServerWithoutKeyStreamsTextAndToolCall(t *testing.T) {
	var path, auth, maxTokensField string
	provider := NewOpenAIProvider(OpenAIConfig{BaseURL: "http://127.0.0.1:8000/v1", Model: "deepseek-v4-flash", ProviderID: "local",
		Compat: DetectCompat("ollama", "http://127.0.0.1:8000/v1")}).(*openAIProvider)
	provider.client = &http.Client{Transport: openAITestRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		path, auth = req.URL.String(), req.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			if _, ok := body[field]; ok {
				maxTokensField = field
			}
		}
		sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Reading.\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.go\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}, nil
	})}
	stream, err := provider.Stream(context.Background(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("read a.go")}}}), StreamOptions{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	result := stream.Result()
	if path != "http://127.0.0.1:8000/v1/chat/completions" || auth != "" || maxTokensField != "max_tokens" {
		t.Fatalf("url=%q auth=%q maxTokensField=%q", path, auth, maxTokensField)
	}
	if result.StopReason != StopReasonToolUse || len(result.Content) != 2 {
		t.Fatalf("result = %#v", result)
	}
	text, _ := result.Content[0].(TextContent)
	call, _ := result.Content[1].(ToolCall)
	if text.Text != "Reading." || call.ID != "c1" || call.Name != "read" || call.Arguments["path"] != "a.go" {
		t.Fatalf("content = %#v", result.Content)
	}
}

// convertMessages lowers the neutral message list into OpenAI Completions wire
// messages. supportsImages gates whether tool-result images are forwarded as a
// trailing user turn (OpenAI tool messages cannot carry image parts).
func convertMessages(messages []Message, supportsImages bool, grammarProps map[string]string) ([]oaiMessage, error) {
	return convertMessagesInternal(messages, supportsImages, grammarProps, false)
}

func convertMessagesInternal(messages []Message, supportsImages bool, grammarProps map[string]string, thinkingAsText bool) ([]oaiMessage, error) {
	return convertCompletionsMessages(messages, completionsConvertOptions{supportsImages: supportsImages, grammarProps: grammarProps, thinkingAsText: thinkingAsText, instructionRole: "system"})
}
