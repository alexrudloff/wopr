package llama

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// requestTimeout bounds every JSON request.
const requestTimeout = 15 * time.Second

// errTimeout is the cause of a request or catalog sync that ran out of time.
var errTimeout = errors.New("request timed out")

// connectionError wraps a transport failure (no HTTP response).
type connectionError struct{ cause error }

func (e *connectionError) Error() string { return "connection failed: " + e.cause.Error() }
func (e *connectionError) Unwrap() error { return e.cause }

// isConnectionError reports whether err means the server could not be reached.
func isConnectionError(err error) bool {
	var connErr *connectionError
	return errors.As(err, &connErr) || errors.Is(err, errTimeout)
}

// fetchResponse is an HTTP status, its headers and the body.
type fetchResponse struct {
	status int
	header http.Header
	body   []byte
}

func (r fetchResponse) ok() bool { return r.status >= 200 && r.status <= 299 }

// fetch performs one request under requestTimeout combined with ctx and reads
// the body.
func fetch(ctx context.Context, method, url string, headers http.Header, body []byte) (fetchResponse, error) {
	requestCtx, cancel := context.WithTimeoutCause(ctx, requestTimeout, errTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(requestCtx, method, url, reader)
	if err != nil {
		return fetchResponse{}, &connectionError{cause: err}
	}
	request.Header = headers
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fetchResponse{}, fetchFailure(ctx, requestCtx, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)
	return fetchResponse{status: response.StatusCode, header: response.Header, body: data}, nil
}

// openStream starts a streaming GET. onSent runs once the request is written
// or the attempt ends, so a caller can order a later request after it.
func openStream(ctx context.Context, url string, headers http.Header, onSent func()) (*http.Response, error) {
	sent := onceFunc(onSent)
	defer sent()
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { sent() }}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, url, nil)
	if err != nil {
		return nil, &connectionError{cause: err}
	}
	request.Header = headers
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fetchFailure(ctx, ctx, err)
	}
	return response, nil
}

func onceFunc(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	return sync.OnceFunc(fn)
}

// fetchFailure maps a transport error to the caller's cancellation cause, the
// timeout, or a connectionError.
func fetchFailure(parent, requestCtx context.Context, err error) error {
	if parent.Err() != nil {
		return context.Cause(parent)
	}
	if requestCtx.Err() != nil {
		return context.Cause(requestCtx)
	}
	return &connectionError{cause: err}
}

// sleep waits for d, or returns the cancellation cause if ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
