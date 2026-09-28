package ai

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func transportErrorTestProvider(baseURL string) Provider {
	return NewOpenAIProvider(OpenAIConfig{
		BaseURL:    baseURL,
		APIKey:     "test-key",
		Model:      "test-model",
		ProviderID: "openai",
	})
}

func transportErrorTestResult(t *testing.T, provider Provider) *AssistantMessage {
	t.Helper()
	transcript := NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hello")}}})
	stream, err := provider.Stream(context.Background(), transcript, StreamOptions{})
	if err != nil {
		return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: err.Error()}
	}
	return stream.Result()
}

func assertRetryableTransportResult(t *testing.T, result *AssistantMessage, category string) {
	t.Helper()
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, category) {
		t.Fatalf("result = reason %q error %q, want transport category %q", result.StopReason, result.ErrorMessage, category)
	}
	if !IsRetryableAssistantError(*result) {
		t.Fatalf("transport result is not retryable: %q", result.ErrorMessage)
	}
}

func closeHTTPConnection(t *testing.T, writer http.ResponseWriter) (net.Conn, *bufio.ReadWriter) {
	t.Helper()
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		t.Fatal("test server does not support hijacking")
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	return connection, buffered
}

func TestProviderTransportFailureBeforeHeadersIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		connection, _ := closeHTTPConnection(t, writer)
		if tcp, ok := connection.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = connection.Close()
	}))
	defer server.Close()

	result := transportErrorTestResult(t, transportErrorTestProvider(server.URL))
	assertRetryableTransportResult(t, result, "network error")
}

func TestProviderTransportFailureMidStreamIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		connection, buffered := closeHTTPConnection(t, writer)
		_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 4096\r\n\r\n")
		_, _ = buffered.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		_ = buffered.Flush()
		_ = connection.Close()
	}))
	defer server.Close()

	result := transportErrorTestResult(t, transportErrorTestProvider(server.URL))
	assertRetryableTransportResult(t, result, "response stream interrupted")
}

func TestProviderHTTPErrorStatusIsNotTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"invalid request"}}`))
	}))
	defer server.Close()

	result := transportErrorTestResult(t, transportErrorTestProvider(server.URL))
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "HTTP 400") {
		t.Fatalf("result = reason %q error %q, want HTTP 400", result.StopReason, result.ErrorMessage)
	}
	if strings.Contains(result.ErrorMessage, "network error") {
		t.Fatalf("HTTP status was mislabeled as a transport failure: %q", result.ErrorMessage)
	}
	if IsRetryableAssistantError(*result) {
		t.Fatalf("HTTP 400 was classified retryable: %q", result.ErrorMessage)
	}
}
