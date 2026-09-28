package ai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// resetProviderRetry restores the package-global retry policy after a test that
// configures it, since the policy is process-global like the HTTP dispatcher.
func resetProviderRetry(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		configuredProviderMaxRetries.Store(0)
		configuredProviderMaxRetryDelayMs.Store(defaultProviderMaxRetryDelayMs)
		providerRetryJitter = defaultProviderRetryJitter
	})
}

// defaultProviderRetryJitter is captured so tests can restore the seam.
var defaultProviderRetryJitter = providerRetryJitter

func hdr(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestIsRetryableProviderResponse(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers http.Header
		want    bool
	}{
		{"x-should-retry true overrides non-retryable status", 400, hdr("x-should-retry", "true"), true},
		{"x-should-retry false overrides retryable status", 500, hdr("x-should-retry", "false"), false},
		{"no response (network error) retries", 0, nil, true},
		{"408 retries", 408, http.Header{}, true},
		{"409 retries", 409, http.Header{}, true},
		{"429 retries", 429, http.Header{}, true},
		{"500 retries", 500, http.Header{}, true},
		{"503 retries", 503, http.Header{}, true},
		{"400 does not retry", 400, http.Header{}, false},
		{"401 does not retry", 401, http.Header{}, false},
		{"404 does not retry", 404, http.Header{}, false},
		{"200 does not retry", 200, http.Header{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableProviderResponse(tc.status, tc.headers); got != tc.want {
				t.Errorf("isRetryableProviderResponse(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// retryTestServer returns an httptest server that emits the given status codes
// in sequence (last repeated), plus a pointer to the attempt counter.
func retryTestServer(t *testing.T, statuses []int, extraHeaders map[string]string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(attempts.Add(1)) - 1
		status := statuses[len(statuses)-1]
		if n < len(statuses) {
			status = statuses[n]
		}
		for k, v := range extraHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts
}

func retryClient() *http.Client {
	return &http.Client{Transport: &retryTransport{base: http.DefaultTransport}}
}

func TestRetryTransport_RetriesRetryableThenSucceeds(t *testing.T) {
	resetProviderRetry(t)
	if err := ConfigureProviderRetry(3, 60000); err != nil {
		t.Fatal(err)
	}
	srv, attempts := retryTestServer(t, []int{503, 503, 200}, map[string]string{"retry-after-ms": "1"})
	resp, err := retryClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestRetryTransport_CapExceededFailsRequest(t *testing.T) {
	resetProviderRetry(t)
	if err := ConfigureProviderRetry(3, 5000); err != nil {
		t.Fatal(err)
	}
	srv, _ := retryTestServer(t, []int{503}, map[string]string{"retry-after": "120"})
	resp, err := retryClient().Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "Server requested 120s retry delay (max: 5s)") {
		t.Fatalf("err = %v; want cap-exceeded failure", err)
	}
}

// recordingRoundTripper is a fake base transport that records the request body
// it received on each attempt and returns a scripted status sequence. It does
// NOT rewind the body, so it isolates retryTransport's own body handling from
// net/http.Transport's internal GetBody rewind.
type recordingRoundTripper struct {
	statuses []int
	bodies   []string
	n        int
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	rt.bodies = append(rt.bodies, body)
	status := rt.statuses[len(rt.statuses)-1]
	if rt.n < len(rt.statuses) {
		status = rt.statuses[rt.n]
	}
	rt.n++
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Retry-After-Ms": {"1"}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// TestRetryTransport_RewindsBodyForRetry proves retryTransport re-sends the full
// request body on each retry independent of the base transport's own rewind.
func TestRetryTransport_RewindsBodyForRetry(t *testing.T) {
	resetProviderRetry(t)
	if err := ConfigureProviderRetry(2, 60000); err != nil {
		t.Fatal(err)
	}
	base := &recordingRoundTripper{statuses: []int{503, 200}}
	rt := &retryTransport{base: base}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://provider.invalid", bytes.NewReader([]byte("payload-body")))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip err = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(base.bodies) != 2 {
		t.Fatalf("base saw %d requests, want 2", len(base.bodies))
	}
	for i, body := range base.bodies {
		if body != "payload-body" {
			t.Errorf("attempt %d body = %q, want %q (body not rewound for retry)", i, body, "payload-body")
		}
	}
}
