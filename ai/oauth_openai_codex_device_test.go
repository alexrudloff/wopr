package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type codexRoundTripper func(*http.Request) (*http.Response, error)

func (f codexRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withMockCodexClient(t *testing.T, handler func(*http.Request) (*http.Response, error)) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: codexRoundTripper(handler)}
	t.Cleanup(func() { http.DefaultClient = old })
}

func codexJSONResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestPollCodexDeviceAuth_403PendingThenComplete(t *testing.T) {
	calls := 0
	withMockCodexClient(t, func(_ *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return codexJSONResp(403, ``), nil // pending
		}
		return codexJSONResp(200, `{"authorization_code":"auth-9","code_verifier":"ver-9"}`), nil
	})
	tok, err := pollCodexDeviceAuth(context.Background(), codexDeviceAuthInfo{deviceAuthID: "d", userCode: "u", intervalSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	if tok.authorizationCode != "auth-9" || tok.codeVerifier != "ver-9" {
		t.Fatalf("token wrong: %+v", tok)
	}
}

func TestLoginOpenAICodexDeviceCode_EndToEnd(t *testing.T) {
	var gotInfo OAuthDeviceCodeInfo
	withMockCodexClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			return codexJSONResp(200, `{"device_auth_id":"d","user_code":"WXYZ-7","interval":0}`), nil
		case "/api/accounts/deviceauth/token":
			return codexJSONResp(200, `{"authorization_code":"ac","code_verifier":"cv"}`), nil
		case "/oauth/token":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "grant_type=authorization_code") {
				t.Errorf("token exchange grant_type missing: %q", body)
			}
			if !strings.Contains(string(body), "code_verifier=cv") {
				t.Errorf("token exchange code_verifier missing: %q", body)
			}
			return codexJSONResp(200, `{"access_token":"acc","refresh_token":"ref","expires_in":3600}`), nil
		}
		t.Fatalf("unexpected path %q", r.URL.Path)
		return nil, nil
	})

	creds, err := LoginOpenAICodexDeviceCode(context.Background(), func(info OAuthDeviceCodeInfo) {
		gotInfo = info
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Access != "acc" || creds.Refresh != "ref" {
		t.Fatalf("creds wrong: %+v", creds)
	}
	if gotInfo.UserCode != "WXYZ-7" || gotInfo.VerificationURI != codexDeviceVerificationURI {
		t.Fatalf("onDeviceCode info wrong: %+v", gotInfo)
	}
	if gotInfo.ExpiresInSeconds != codexDeviceCodeTimeoutSeconds {
		t.Fatalf("expiresInSeconds = %v, want %d", gotInfo.ExpiresInSeconds, codexDeviceCodeTimeoutSeconds)
	}
}
