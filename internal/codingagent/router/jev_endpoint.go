package router

import (
	"net/url"
	"os"
	"strings"
)

// Jev endpoints speak one shape: POST <base>/v1/systemone with a Bearer key.
// TypeSafe serves it at https://api.typesafe.ai and OpenRouter at
// https://openrouter.ai/api; a self-hosted proxy may hold the upstream key
// itself and take none.

// JevKeyProvider is the credential id wopr stores a Jev API key under.
const JevKeyProvider = "typesafe"

// NormalizeJevEndpoint turns a base URL or a full URL into the decisions
// endpoint: a URL ending in /systemone is kept, openrouter.ai goes to its
// /api/v1/systemone, and any other base gets /v1/systemone (or /systemone
// after a trailing /v1). A missing scheme means https.
func NormalizeJevEndpoint(raw string) string {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	if strings.HasSuffix(s, "/systemone") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	if isOpenRouterHost(u.Host) {
		switch u.Path {
		case "", "/api", "/api/v1":
			return u.Scheme + "://" + u.Host + "/api/v1/systemone"
		}
	}
	if strings.HasSuffix(u.Path, "/v1") {
		return s + "/systemone"
	}
	return s + "/v1/systemone"
}

// IsOpenRouterEndpoint reports whether endpoint is OpenRouter's.
func IsOpenRouterEndpoint(endpoint string) bool {
	u, err := url.Parse(NormalizeJevEndpoint(endpoint))
	return err == nil && isOpenRouterHost(u.Host)
}

func isOpenRouterHost(host string) bool {
	host = strings.ToLower(host)
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}

// requestModel is the model id sent to endpoint. OpenRouter names Jev
// models by author: jev-latest is ~typesafe/jev-latest, and other ids get
// the typesafe/ prefix. Prefixed ids and other endpoints are unchanged.
func requestModel(endpoint, model string) string {
	if !IsOpenRouterEndpoint(endpoint) || strings.Contains(model, "/") {
		return model
	}
	if model == "jev-latest" {
		return "~typesafe/jev-latest"
	}
	return "typesafe/" + model
}

// ResolveJevKey returns the key cfg's endpoint is called with, or "" to
// send no Authorization header. lookup returns a provider's stored key.
//
// "none" never sends a key; another provider id uses that provider's key.
// Unset (or JevKeyProvider) uses the key saved for Jev, then
// TYPESAFE_API_KEY; for an OpenRouter endpoint, then the OpenRouter key
// (stored or OPENROUTER_API_KEY).
func ResolveJevKey(cfg JevConfig, lookup func(provider string) string) string {
	switch cfg.APIKeyProvider {
	case "none":
		return ""
	case "", JevKeyProvider:
	default:
		return strings.TrimSpace(lookup(cfg.APIKeyProvider))
	}
	if key := strings.TrimSpace(lookup(JevKeyProvider)); key != "" {
		return key
	}
	if IsOpenRouterEndpoint(cfg.Endpoint) {
		if key := strings.TrimSpace(lookup("openrouter")); key != "" {
			return key
		}
		return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	}
	return strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
}

// namedKeyProvider reports whether cfg names a provider whose key is
// required (not "none" and not the automatic Jev key).
func namedKeyProvider(cfg JevConfig) bool {
	switch cfg.APIKeyProvider {
	case "", "none", JevKeyProvider:
		return false
	}
	return true
}
