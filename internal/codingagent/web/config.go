// Package web implements the web_fetch and web_search tools: a URL fetched
// to markdown or text, with private networks blocked by default, and a web
// search through a configured provider (Brave, Tavily or SearXNG).
package web

import (
	"cmp"
	"encoding/json"
	"strings"
	"time"
)

// Default limits.
const (
	DefaultFetchTimeout = 30 * time.Second
	DefaultMaxBytes     = 5 << 20
	DefaultMaxResults   = 5
)

// Config is the web settings section.
type Config struct {
	// AllowPrivateNetwork lets web_fetch reach loopback, private and
	// link-local addresses.
	AllowPrivateNetwork bool `json:"allowPrivateNetwork,omitempty"`
	// FetchTimeoutMs bounds one fetch; MaxBytes bounds the downloaded body.
	FetchTimeoutMs int          `json:"fetchTimeoutMs,omitempty"`
	MaxBytes       int          `json:"maxBytes,omitempty"`
	Search         SearchConfig `json:"search"`
}

// SearchConfig selects the web_search backend.
type SearchConfig struct {
	// Provider is brave, tavily or searxng. Empty picks the first provider
	// whose environment variable is set.
	Provider string `json:"provider,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
	// URL is the SearXNG instance. Brave and Tavily use their fixed
	// endpoints, so an API key only ever goes to its own provider.
	URL        string `json:"url,omitempty"`
	MaxResults int    `json:"maxResults,omitempty"`
}

// ParseConfig reads the global web section, then lets the project section
// override the fields it sets.
func ParseConfig(global, project json.RawMessage) Config {
	var cfg Config
	for _, raw := range []json.RawMessage{global, project} {
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cfg)
		}
	}
	return cfg
}

func (c Config) fetchTimeout() time.Duration {
	if c.FetchTimeoutMs > 0 {
		return time.Duration(c.FetchTimeoutMs) * time.Millisecond
	}
	return DefaultFetchTimeout
}

func (c Config) maxBytes() int {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return DefaultMaxBytes
}

// searchEnv lists each provider's environment variables in lookup order.
var searchEnv = []struct{ provider, key string }{
	{"brave", "BRAVE_API_KEY"},
	{"brave", "BRAVE_SEARCH_API_KEY"},
	{"tavily", "TAVILY_API_KEY"},
	{"searxng", "SEARXNG_URL"},
}

// SearchEnvVars lists the environment variables that configure web search;
// hermetic tests clear them.
func SearchEnvVars() []string {
	out := make([]string, len(searchEnv))
	for i, env := range searchEnv {
		out[i] = env.key
	}
	return out
}

// resolved fills Provider and the key or URL from the environment when the
// settings leave them empty.
func (c SearchConfig) resolved(lookup func(string) string) SearchConfig {
	out := c
	out.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	for _, env := range searchEnv {
		if out.Provider != "" && out.Provider != env.provider {
			continue
		}
		v := lookup(env.key)
		if v == "" {
			continue
		}
		if env.provider == "searxng" {
			out.URL = cmp.Or(out.URL, v)
		} else if out.APIKey == "" {
			out.APIKey = v
		}
		out.Provider = cmp.Or(out.Provider, env.provider)
		break
	}
	if out.MaxResults <= 0 || out.MaxResults > 20 {
		out.MaxResults = DefaultMaxResults
	}
	return out
}
