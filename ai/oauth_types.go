package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// OAuthCredentials holds the tokens returned by an OAuth flow.
type OAuthCredentials struct {
	Refresh string `json:"refresh"`
	Access  string `json:"access"`
	Expires int64  `json:"expires"` // Unix millis
	// ProjectID is used by Google Cloud Code Assist / Antigravity OAuth.
	// It is the only extra credential field in use, modeled explicitly so
	// auth.json round-trips the Google credential shape.
	ProjectID string `json:"projectId,omitempty"`
}

// OAuthPrompt describes an interactive prompt during the OAuth flow.
type OAuthPrompt struct {
	Message     string
	Placeholder string
	AllowEmpty  bool
}

// OAuthAuthInfo is the URL + instructions presented to the user.
type OAuthAuthInfo struct {
	URL          string
	Instructions string
}

// OAuthDeviceCodeInfo is the user code + verification URL presented during a
// device-code (RFC 8628) login flow.
type OAuthDeviceCodeInfo struct {
	UserCode         string
	VerificationURI  string
	IntervalSeconds  float64
	ExpiresInSeconds float64
}

// OAuthSelectOption is one selectable choice in an OAuth flow.
type OAuthSelectOption struct {
	ID    string
	Label string
}

// OAuthSelectPrompt describes an interactive selection prompt during the OAuth flow.
type OAuthSelectPrompt struct {
	Message string
	Options []OAuthSelectOption
}

// OAuthLoginCallbacks groups the callbacks used during an OAuth login flow.
type OAuthLoginCallbacks struct {
	OnAuth                   func(info OAuthAuthInfo)
	OnDeviceCode             func(info OAuthDeviceCodeInfo)
	OnPrompt                 func(prompt OAuthPrompt) (string, error)
	OnPromptContext          func(context.Context, OAuthPrompt) (string, error)
	OnProgress               func(message string)
	OnManualCodeInput        func() (string, error)
	OnManualCodeInputContext func(context.Context) (string, error)
	OnSelect                 func(prompt OAuthSelectPrompt) (string, error)
	OnSelectContext          func(context.Context, OAuthSelectPrompt) (string, error)
}

// OAuthCredentialStatus describes a stored credential owned by a registered
// OAuth provider outside auth.json. AuthType is "oauth" or "api_key" and Source
// is rendered by the login selector (for example "stored").
type OAuthCredentialStatus struct {
	AuthType string
	Source   string
}

// OAuthCredentialStore is an optional extension hook for OAuth providers whose
// credentials live outside auth.json. Built-in providers do not need
// it; product extensions can implement it to keep their existing credential
// stores while still participating in the generic /login and /logout surfaces.
type OAuthCredentialStore interface {
	OAuthCredentialStatus() (OAuthCredentialStatus, bool)
	StoreOAuthCredentials(creds OAuthCredentials) (path string, err error)
	DeleteOAuthCredentials() (deleted bool, err error)
}

// OAuthProviderInterface is the contract for an OAuth provider.
type OAuthProviderInterface interface {
	// ID returns the provider identifier (e.g. "anthropic").
	ID() string
	// Name returns the human-readable provider name.
	Name() string
	// UsesCallbackServer returns true if the flow uses a local HTTP callback.
	UsesCallbackServer() bool
	// Login runs the OAuth authorization flow; ctx cancels it.
	Login(ctx context.Context, callbacks OAuthLoginCallbacks) (OAuthCredentials, error)
	// RefreshToken refreshes expired credentials; ctx cancels it.
	RefreshToken(ctx context.Context, creds OAuthCredentials) (OAuthCredentials, error)
	// GetAPIKey extracts the bearer token from credentials.
	GetAPIKey(creds OAuthCredentials) string
}

// OAuthSubscriptionProvider marks an OAuth login billed by a subscription.
type OAuthSubscriptionProvider interface {
	IsSubscription() bool
}

// IsOAuthSubscriptionProvider reports whether the provider's OAuth login is a
// subscription login.
func IsOAuthSubscriptionProvider(providerID string) bool {
	provider, ok := GetOAuthProvider(providerID)
	if !ok {
		return false
	}
	subscription, ok := provider.(OAuthSubscriptionProvider)
	return ok && subscription.IsSubscription()
}

// oauthPost POSTs body with the given Content-Type, accepting JSON, and
// returns the status and the response body.
func oauthPost(ctx context.Context, client *http.Client, endpoint, contentType, body string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

// oauthBodySuffix returns ": " + body, or "" for an empty body.
func oauthBodySuffix(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return ": " + string(body)
}

// writeOAuthHTML writes an OAuth callback page with the given status.
func writeOAuthHTML(w http.ResponseWriter, status int, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, html)
}

type manualInput struct {
	val string
	err error
}

// manualCodeInput runs read in the background and delivers its result on
// the returned channel.
func manualCodeInput(read func() (string, error)) <-chan manualInput {
	ch := make(chan manualInput, 1)
	go func() {
		v, err := read()
		ch <- manualInput{v, err}
	}()
	return ch
}
