package ai

// Covers system-message replay in the transcript, plus the
// fold-without-native-support payload cases for each provider family.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func replayTool(name string, description ...string) ToolSchema {
	text := name + " tool"
	if len(description) > 0 {
		text = description[0]
	}
	return ToolSchema{Name: name, Description: text, Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
}

// foldContext is a history where a later system
// message rewrites sections, removes the base tool, and adds another.
func foldContext() TranscriptContext {
	return NormalizeContext(Context{Messages: []Message{
		SystemMessage{
			Content:    SystemText("base prompt"),
			Sections:   OrderedSections{{Name: "rules", Value: new("<rules>\nold rules\n</rules>")}, {Name: "docs", Value: new("<docs>\nread docs\n</docs>")}},
			ToolsAdded: []ToolSchema{replayTool("base_tool")},
		},
		UserMessage{Content: UserText("before"), Timestamp: 1},
		SystemMessage{
			Content:      SystemText("updated guidance"),
			Sections:     OrderedSections{{Name: "rules", Value: new("<rules>\nnew rules\n</rules>")}, {Name: "docs", Value: nil}},
			ToolsRemoved: []ToolReference{{Name: "base_tool"}},
			ToolsAdded:   []ToolSchema{replayTool("late_tool")},
			Timestamp:    2,
		},
	}})
}

const foldedPrompt = "base prompt\n\nupdated guidance\n\n<rules>\nnew rules\n</rules>"

// capturePayloadVia installs a transport that records the JSON request body
// and answers with an empty stream, then drains the provider stream.
func capturePayloadVia(t *testing.T, install func(*http.Client), stream func() (*AssistantMessageEventStream, error), body string) map[string]any {
	t.Helper()
	var payload map[string]any
	install(&http.Client{Transport: responsesTestRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	result, err := stream()
	if err != nil {
		t.Fatal(err)
	}
	for range result.Events(context.Background()) {
	}
	return payload
}

func payloadNames(t *testing.T, values any, path ...string) []string {
	t.Helper()
	items, _ := values.([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		value := item
		for _, key := range path {
			value = value.(map[string]any)[key]
		}
		name, _ := value.(string)
		names = append(names, name)
	}
	return names
}

func TestTranscriptFoldsAnthropicUpdatesIntoSystemPromptWithoutNativeSupport(t *testing.T) {
	for _, compat := range []*ModelCompat{nil, {SupportsMidConvoToolChanges: new(true)}} {
		provider := NewAnthropicProvider(AnthropicConfig{APIKey: "test", Model: "custom-claude", ProviderID: "anthropic", Compat: compat}).(*anthropicProvider)
		payload := capturePayloadVia(t, func(client *http.Client) { provider.client = client }, func() (*AssistantMessageEventStream, error) {
			return provider.Stream(context.Background(), foldContext(), StreamOptions{})
		}, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

		if got := payloadNames(t, payload["system"], "text"); !slices.Equal(got, []string{foldedPrompt}) {
			t.Errorf("compat %+v system = %q", compat, got)
		}
		if got := payloadNames(t, payload["tools"], "name"); !slices.Equal(got, []string{"late_tool"}) {
			t.Errorf("compat %+v tools = %v", compat, got)
		}
		if got := payloadNames(t, payload["messages"], "role"); !slices.Equal(got, []string{"user"}) {
			t.Errorf("compat %+v message roles = %v", compat, got)
		}
	}
}

func TestTranscriptFoldsOpenAIResponsesUpdatesIntoLeadingDeveloperMessage(t *testing.T) {
	provider := NewOpenAIResponsesProvider(OpenAIResponsesConfig{APIKey: "test", Model: "custom-model", ProviderID: "openai", IsReasoning: true}).(*openAIResponsesProvider)
	payload := capturePayloadVia(t, func(client *http.Client) { provider.client = client }, func() (*AssistantMessageEventStream, error) {
		return provider.Stream(context.Background(), foldContext(), StreamOptions{})
	}, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")

	if got := payloadNames(t, payload["tools"], "name"); !slices.Equal(got, []string{"late_tool"}) {
		t.Errorf("tools = %v", got)
	}
	input, _ := payload["input"].([]any)
	if got := payloadNames(t, input, "role"); !slices.Equal(got, []string{"developer", "user"}) {
		t.Errorf("input roles = %v", got)
	}
	if first, _ := input[0].(map[string]any); first["content"] != foldedPrompt {
		t.Errorf("developer content = %#v", first["content"])
	}
}

func TestTranscriptFoldsOpenAICompletionsUpdatesIntoSystemPrompt(t *testing.T) {
	provider := &openAIProvider{cfg: OpenAIConfig{BaseURL: "https://example.test/v1", APIKey: "test", Model: "custom-model", ProviderID: "custom-provider"}}
	payload := capturePayloadVia(t, func(client *http.Client) { provider.client = client }, func() (*AssistantMessageEventStream, error) {
		return provider.Stream(context.Background(), foldContext(), StreamOptions{})
	}, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")

	if got := payloadNames(t, payload["tools"], "function", "name"); !slices.Equal(got, []string{"late_tool"}) {
		t.Errorf("tools = %v", got)
	}
	messages, _ := payload["messages"].([]any)
	if got := payloadNames(t, messages, "role"); !slices.Equal(got, []string{"system", "user"}) {
		t.Errorf("roles = %v", got)
	}
	if first, _ := messages[0].(map[string]any); first["content"] != foldedPrompt {
		t.Errorf("system content = %#v", first["content"])
	}
}
