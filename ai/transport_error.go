package ai

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// transportError is a network failure between wopr and a provider: before the
// response headers, or while reading the response stream. Its message starts
// with "network error", which retry classification treats as transient.
type transportError struct {
	stream bool
	cause  error
}

func (err *transportError) Error() string {
	if err.stream {
		return "network error: response stream interrupted: " + err.cause.Error()
	}
	return "network error: " + err.cause.Error()
}

func (err *transportError) Unwrap() error { return err.cause }

func contextTransportError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

// providerTransport wraps network failures in transportError and records
// subscription usage headers. HTTP responses, including error statuses,
// remain responses; cancellation surfaces as the context's error.
type providerTransport struct {
	base http.RoundTripper
}

func (transport *providerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if contextErr := contextTransportError(request.Context()); contextErr != nil {
			return response, contextErr
		}
		return response, &transportError{cause: err}
	}
	recordSubscriptionHeaders(response.Header, time.Now())
	if response.Body != nil {
		response.Body = &providerResponseBody{ReadCloser: response.Body, ctx: request.Context()}
	}
	return response, nil
}

type providerResponseBody struct {
	io.ReadCloser
	ctx context.Context
}

func (body *providerResponseBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if err == nil || errors.Is(err, io.EOF) {
		return count, err
	}
	if contextErr := contextTransportError(body.ctx); contextErr != nil {
		return count, contextErr
	}
	return count, &transportError{stream: true, cause: err}
}

type connectionError interface {
	ConnectionError() bool
}

// mapBedrockTransportError applies transportError at the AWS SDK boundary.
// Modeled HTTP/service errors remain untouched so status and Bedrock exception
// classification continue to control overflow and retry behavior.
func mapBedrockTransportError(ctx context.Context, err error, stream bool) error {
	if err == nil {
		return nil
	}
	if contextErr := contextTransportError(ctx); contextErr != nil {
		return contextErr
	}
	if !isGoTransportError(err) {
		return err
	}
	return &transportError{stream: stream, cause: err}
}

func isGoTransportError(err error) bool {
	var connection connectionError
	if errors.As(err, &connection) && connection.ConnectionError() {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	// HTTP/2 errors in net/http are intentionally unexported and can reach an SDK stream without a net.Error in their chain. Keep this fallback limited to Go transport messages rather than broad provider text.
	text := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"connection reset by peer",
		"use of closed network connection",
		"http2: server sent goaway",
		"http2: client connection lost",
		"http2 stream closed",
		"tls handshake timeout",
	} {
		if strings.Contains(text, fragment) {
			return true
		}
	}
	return false
}
