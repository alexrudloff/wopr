package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// orRoundTripper intercepts only the OpenRouter token endpoint, letting
// loopback callback requests reach the real transport.
type orRoundTripper struct {
	handler func(*http.Request) (*http.Response, error)
}

func (rt orRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "openrouter.ai" {
		return rt.handler(r)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func withOpenRouterToken(t *testing.T, handler func(*http.Request) (*http.Response, error)) {
	t.Helper()
	prev := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: orRoundTripper{handler: handler}}
	t.Cleanup(func() { http.DefaultClient = prev })
}

func cannedResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestOpenRouterExchangeSuccess(t *testing.T) {
	var gotBody map[string]string
	withOpenRouterToken(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != openRouterTokenURL {
			t.Fatalf("unexpected exchange request %s %s", r.Method, r.URL)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		return cannedResp(200, `{"key":"sk-or-permanent"}`), nil
	})

	creds, err := exchangeOpenRouterCode(t.Context(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if creds.Access != "sk-or-permanent" || creds.Refresh != "" {
		t.Fatalf("creds = %+v", creds)
	}
	if creds.Expires != openRouterKeyExpiry {
		t.Fatalf("expires = %d, want MAX_SAFE_INTEGER %d", creds.Expires, openRouterKeyExpiry)
	}
	if gotBody["code"] != "the-code" || gotBody["code_verifier"] != "the-verifier" || gotBody["code_challenge_method"] != "S256" {
		t.Fatalf("PKCE exchange body = %+v", gotBody)
	}
}

// TestOpenRouterClaimedCallbackWinsOverManual drives a real loopback callback
// that claims and completes the exchange before manual paste resolves; the
// browser credential must win.
func TestOpenRouterClaimedCallbackWinsOverManual(t *testing.T) {
	withOpenRouterToken(t, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		return cannedResp(200, `{"key":"key-for-`+body["code"]+`"}`), nil
	})

	creds, err := LoginOpenRouter(t.Context(), OAuthLoginCallbacks{
		OnAuth: func(info OAuthAuthInfo) {
			// Synchronously complete the browser callback before manual runs.
			u, perr := url.Parse(info.URL)
			if perr != nil {
				t.Errorf("authorize url: %v", perr)
				return
			}
			cbURL := u.Query().Get("callback_url")
			resp, gerr := http.Get(cbURL + "?code=BROWSER")
			if gerr != nil {
				t.Errorf("browser callback: %v", gerr)
				return
			}
			_ = resp.Body.Close()
		},
		OnManualCodeInput: func() (string, error) {
			return "code=MANUAL", nil
		},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if creds.Access != "key-for-BROWSER" {
		t.Fatalf("claimed callback did not win over manual: %+v", creds)
	}
}

func TestOpenRouterRefreshKeepsPermanentKey(t *testing.T) {
	// An OpenRouter API key does not expire and has no refresh token.
	creds := OAuthCredentials{Access: "sk", Expires: openRouterKeyExpiry}
	refreshed, err := OpenRouterOAuthProvider{}.RefreshToken(context.Background(), creds)
	if err != nil || refreshed != creds {
		t.Fatalf("refresh changed a permanent key: %+v %v", refreshed, err)
	}
}
