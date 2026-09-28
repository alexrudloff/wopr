package ai

import "testing"

func retryFaux(text string, stopReason StopReason, errorMessage string) AssistantMessage {
	message := AssistantMessage{Content: []AssistantContentBlock{}, StopReason: stopReason, ErrorMessage: errorMessage}
	if text != "" {
		message.Content = append(message.Content, TextContent{Text: text})
	}
	if stopReason == "" {
		message.StopReason = StopReasonStop
	}
	return message
}

func errorFaux(errorMessage string) AssistantMessage {
	return retryFaux("", StopReasonError, errorMessage)
}

func TestIsRetryableAssistantErrorClassification(t *testing.T) {
	retryable := []string{
		"An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID req_******** in your message.",
		`{"message":"The system encountered an unexpected error during processing. Try your request again."}`,
		"ResourceExhausted: Worker local total request limit reached (288/48)",
		"The socket connection was closed unexpectedly. For more information, pass `verbose: true` in the second argument to fetch()",
		"Error: exceeded request buffer limit while retrying upstream",
		"network error: dial tcp: lookup api.example.com: no such host",
		"OpenAI Responses stream ended before a terminal response event",
		"The system is currently experiencing high demand and cannot process your request. Your request exceeds the maximum usage size allowed during peak load. For improved capacity reliability, consider switching to Provisioned Throughput.",
		"overloaded_error",
		"520 status code (no body)",
		"524 status code (no body)",
	}
	for _, message := range retryable {
		if !IsRetryableAssistantError(errorFaux(message)) {
			t.Errorf("expected retryable: %q", message)
		}
	}
	if IsRetryableAssistantError(errorFaux("429 quota exceeded")) {
		t.Error("provider limit error must stay non-retryable")
	}
	if IsRetryableAssistantError(retryFaux("not an error", "", "")) {
		t.Error("successful message classified retryable")
	}
}
