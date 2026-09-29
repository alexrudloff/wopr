package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func fetch(t *testing.T, tool *FetchTool, rawURL string) (string, error) {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"url": rawURL})
	res, err := tool.Execute(context.Background(), "call-1", params, nil)
	return res.Content, err
}

func serve(t *testing.T, contentType, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const samplePage = `<html><head><title>  Sample   Page </title><script>alert(1)</script></head><body><h1>Getting <em>started</em></h1></body></html>`

func TestFetchConvertsHTMLAndPassesTextThrough(t *testing.T) {
	tool := NewFetchTool(Config{AllowPrivateNetwork: true})
	out, err := fetch(t, tool, serve(t, "text/html; charset=utf-8", samplePage, 200).URL)
	if err != nil || !strings.HasPrefix(out, "# Sample Page\nURL: http://127.0.0.1") || !strings.Contains(out, "# Getting *started*") {
		t.Errorf("html fetch = %q, %v", out, err)
	}
	out, err = fetch(t, tool, serve(t, "application/json", `{"a":1}`, 200).URL)
	if err != nil || !strings.HasSuffix(out, `{"a":1}`) {
		t.Errorf("json fetch = %q, %v", out, err)
	}
	latin1 := serve(t, "text/plain; charset=iso-8859-1", "caf\xe9", 200)
	if out, err = fetch(t, tool, latin1.URL); err != nil || !strings.HasSuffix(out, "café") {
		t.Errorf("latin-1 fetch = %q, %v", out, err)
	}
}

func TestFetchBlocksPrivateNetworksByDefault(t *testing.T) {
	srv := serve(t, "text/plain", "internal", 200)
	_, err := fetch(t, NewFetchTool(Config{}), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("loopback fetch error = %v, want it blocked", err)
	}
	// A public page that redirects to a private address is checked per hop,
	// which the dial-time guard covers; the loopback case above exercises it.
	for addr, blocked := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "192.168.0.1": true, "172.16.5.4": true, "169.254.169.254": true,
		"100.64.0.1": true, "0.0.0.0": true, "::1": true, "fe80::1": true, "fc00::1": true, "::ffff:127.0.0.1": true,
		"93.184.216.34": false, "2606:4700::1111": false,
	} {
		if got := blockedAddr(netip.MustParseAddr(addr)); got != blocked {
			t.Errorf("blockedAddr(%s) = %t, want %t", addr, got, blocked)
		}
	}
}

// A DuckDuckGo results page yields its organic results with redirect links
// unwrapped; a page without results (a rate limit or challenge) says so
// instead of coming back blank.
func TestDuckDuckGoSearch(t *testing.T) {
	page, err := os.ReadFile("testdata/ddg_results.html")
	if err != nil {
		t.Fatal(err)
	}
	search := func(body string) (string, bool) {
		t.Helper()
		srv := serve(t, "text/html", body, http.StatusOK)
		tool := NewSearchTool(SearchConfig{}, func(string) string { return "" })
		tool.Backend.endpoint = srv.URL
		res, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"query":"rotating detonation engine"}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content, res.IsError
	}
	got, isErr := search(string(page))
	if isErr || !strings.Contains(got, "1. Integrated Rotating Detonation Engine System (InRoDES) - NASA\n   https://www.nasa.gov/integrated-rotating-detonation-engine-system-inrodes/") ||
		!strings.Contains(got, "2. ") || !strings.Contains(got, "https://techport.nasa.gov/projects/116281") || strings.Contains(got, "Sponsored") {
		t.Fatalf("results = %q", got)
	}
	got, isErr = search(`<html><body><div class="anomaly-modal">Please confirm you are human</div></body></html>`)
	if !isErr || !strings.Contains(got, "possibly rate-limited") {
		t.Fatalf("challenge page = %q, error %v", got, isErr)
	}
}
