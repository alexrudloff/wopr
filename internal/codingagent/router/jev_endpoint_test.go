package router

import "testing"

// Endpoint and model forms people will paste for TypeSafe, OpenRouter, and
// proxies all reach the decisions endpoint with the id that host expects.
func TestJevEndpointAndModelForms(t *testing.T) {
	for _, tc := range []struct{ in, endpoint, model, sent string }{
		{"https://api.typesafe.ai", "https://api.typesafe.ai/v1/systemone", "jev-1.13", "jev-1.13"},
		{"api.typesafe.ai/", "https://api.typesafe.ai/v1/systemone", "jev-latest", "jev-latest"},
		{"https://openrouter.ai/api", "https://openrouter.ai/api/v1/systemone", "jev-1.13", "typesafe/jev-1.13"},
		{"openrouter.ai", "https://openrouter.ai/api/v1/systemone", "jev-latest", "~typesafe/jev-latest"},
		{"https://openrouter.ai/api/v1/systemone", "https://openrouter.ai/api/v1/systemone", "typesafe/jev-1.13", "typesafe/jev-1.13"},
		{"http://proxy.lan:8001/v1/systemone", "http://proxy.lan:8001/v1/systemone", "jev-1.13", "jev-1.13"},
		{"http://proxy.lan:8001", "http://proxy.lan:8001/v1/systemone", "jev-1.13", "jev-1.13"},
		{"http://proxy.lan:8001/v1", "http://proxy.lan:8001/v1/systemone", "jev-1.13", "jev-1.13"},
	} {
		got := NormalizeJevEndpoint(tc.in)
		if got != tc.endpoint {
			t.Errorf("NormalizeJevEndpoint(%q) = %q, want %q", tc.in, got, tc.endpoint)
		}
		if sent := requestModel(got, tc.model); sent != tc.sent {
			t.Errorf("requestModel(%q, %q) = %q, want %q", got, tc.model, sent, tc.sent)
		}
	}

	// A key is only sent where the user asked for one: "none" never sends,
	// and an OpenRouter key never goes to another host.
	lookup := func(p string) string {
		return map[string]string{"openrouter": "or-key"}[p]
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	for _, tc := range []struct {
		cfg  JevConfig
		want string
	}{
		{JevConfig{Endpoint: "https://openrouter.ai/api"}, "or-key"},
		{JevConfig{Endpoint: "https://openrouter.ai/api", APIKeyProvider: "none"}, ""},
		{JevConfig{Endpoint: "http://proxy.lan:8001"}, ""},
		{JevConfig{Endpoint: "https://api.typesafe.ai"}, ""},
	} {
		if got := ResolveJevKey(tc.cfg, lookup); got != tc.want {
			t.Errorf("ResolveJevKey(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}
