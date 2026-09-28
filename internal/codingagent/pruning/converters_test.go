package pruning

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// captureBody sends messages through provider against a local server and
// returns the request body the provider built.
func captureBody(t *testing.T, build func(baseURL string) ai.Provider, messages []agent.AgentMessage) map[string]any {
	t.Helper()
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		select {
		case bodies <- data:
		default:
		}
		http.Error(w, `{"error":{"type":"invalid_request_error","message":"captured"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	provider := build(server.URL)
	transcript := ai.NormalizeContext(ai.Context{SystemPrompt: "system", Messages: agent.ConvertToLLM(messages, nil)})
	stream, err := provider.Stream(context.Background(), transcript, ai.StreamOptions{})
	if err == nil && stream != nil {
		for range stream.Events(context.Background()) {
		}
	}
	var body []byte
	select {
	case body = <-bodies:
	default:
		t.Fatalf("provider sent no request (err %v)", err)
	}
	if strings.Contains(string(body), "No result provided") {
		t.Fatal("the converter had to invent a tool result: a pruned call lost its pair")
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// prunedFixture applies every kind of edit: dedupe, stale read, failed-call
// purge and a compress hosted by an assistant message.
func prunedFixture(t *testing.T) []agent.AgentMessage {
	f := newFixture(t)
	f.user("work on a.go")
	f.call("read", ai.JsonObject{"path": "a.go"}, big("a"), false)
	f.call("grep", ai.JsonObject{"pattern": "x"}, big("g"), false)
	f.call("read", ai.JsonObject{"path": "a.go"}, big("a"), false)
	huge := strings.Repeat("no match here\n", 100)
	f.call("edit", ai.JsonObject{"path": "a.go", "edits": []any{map[string]any{"oldText": huge, "newText": huge}}}, "Could not find the text", true)
	f.call("write", ai.JsonObject{"path": "a.go", "content": "package a"}, "Wrote a.go", false)
	f.call("ls", ai.JsonObject{"path": "."}, big("ls"), false)
	f.text("done so far")
	f.user("continue")
	f.call("read", ai.JsonObject{"path": "b.go"}, big("b"), false)
	edits := Plan(DefaultConfig(), f.items(), "/repo")
	if got := strategies(edits); got[StrategyDedupe] == 0 || got[StrategySupersede] == 0 || got[StrategyPurgeErrors] == 0 {
		t.Fatalf("fixture strategies = %v, want all three", got)
	}
	f.apply(edits)
	plan, err := PlanCompress(f.items(), 3, 7, "grep found x in a.go; a.go was rewritten", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.apply(plan.Edits)
	messages := f.messages()
	checkPairing(t, messages)
	return messages
}

func TestPrunedContextPairsForAnthropic(t *testing.T) {
	body := captureBody(t, func(url string) ai.Provider {
		return ai.NewAnthropicProvider(ai.AnthropicConfig{APIKey: "test", Model: "claude-sonnet-4-5", BaseURL: url})
	}, prunedFixture(t))
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("no messages in %v", body)
	}
	var pending map[string]bool
	for i, raw := range messages {
		message := raw.(map[string]any)
		blocks, _ := message["content"].([]any)
		results := map[string]bool{}
		calls := map[string]bool{}
		for _, b := range blocks {
			block := b.(map[string]any)
			switch block["type"] {
			case "tool_use":
				calls[block["id"].(string)] = true
			case "tool_result":
				results[block["tool_use_id"].(string)] = true
			}
		}
		for id := range results {
			if !pending[id] {
				t.Fatalf("message %d: tool_result %s does not answer the previous assistant", i, id)
			}
			delete(pending, id)
		}
		if len(pending) > 0 {
			t.Fatalf("message %d: tool_use %v unanswered", i, pending)
		}
		if message["role"] == "assistant" {
			pending = calls
		}
	}
}

func TestPrunedContextPairsForOpenAI(t *testing.T) {
	body := captureBody(t, func(url string) ai.Provider {
		return ai.NewOpenAIProvider(ai.OpenAIConfig{APIKey: "test", Model: "gpt-4o", BaseURL: url})
	}, prunedFixture(t))
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("no messages in %v", body)
	}
	pending := map[string]bool{}
	for i, raw := range messages {
		message := raw.(map[string]any)
		switch message["role"] {
		case "tool":
			id, _ := message["tool_call_id"].(string)
			if !pending[id] {
				t.Fatalf("message %d: tool message %s has no call", i, id)
			}
			delete(pending, id)
		default:
			if len(pending) > 0 {
				t.Fatalf("message %d: tool calls %v unanswered", i, pending)
			}
			calls, _ := message["tool_calls"].([]any)
			for _, c := range calls {
				pending[c.(map[string]any)["id"].(string)] = true
			}
		}
	}
	if len(pending) > 0 {
		t.Fatalf("tool calls %v unanswered at the end", pending)
	}
}
