package ai

import (
	"context"
	"strings"
	"testing"
)

// A non-OpenAI provider without a base URL must fail instead of sending its
// credential to api.openai.com.
func TestOpenAICompatibleProviderWithoutBaseURLNeverDefaultsToOpenAI(t *testing.T) {
	provider := NewOpenAIProvider(OpenAIConfig{APIKey: "hf_secret", Model: "some/model", ProviderID: "huggingface"})
	_, err := provider.Stream(context.Background(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserContentBlocks{TextContent{Text: "hi"}}}}}), StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "no base URL") {
		t.Fatalf("expected a missing base URL error, got %v", err)
	}
	if openai := NewOpenAIProvider(OpenAIConfig{Model: "gpt"}).(*openAIProvider); openai.cfg.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("OpenAI itself should still default, got %q", openai.cfg.BaseURL)
	}
}
