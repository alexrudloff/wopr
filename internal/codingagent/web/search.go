package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// SearchToolName is the search tool's name.
const SearchToolName = "web_search"

const (
	snippetLimit  = 300
	searchTimeout = 20 * time.Second
	braveEndpoint = "https://api.search.brave.com/res/v1/web/search"
	tavilyEndpont = "https://api.tavily.com/search"
)

// Result is one search hit.
type Result struct {
	Title, URL, Snippet string
}

// ddgName is the keyless backend used when no provider is configured.
const ddgName = "duckduckgo"

// Backend runs searches against one provider's HTTP API, or DuckDuckGo's
// results page when none is configured.
type Backend struct {
	name     string
	endpoint string
	key      string
	private  bool
	client   *http.Client
}

func newBackend(cfg SearchConfig) *Backend {
	b := &Backend{name: cfg.Provider, key: cfg.APIKey, client: &http.Client{Timeout: searchTimeout}}
	switch cfg.Provider {
	case "brave":
		b.endpoint = braveEndpoint
	case "tavily":
		b.endpoint = tavilyEndpont
	case "searxng":
		if cfg.URL != "" {
			b.endpoint = strings.TrimRight(cfg.URL, "/") + "/search"
			b.private = cfg.Private
			return b
		}
	}
	if b.endpoint == "" || (b.name != "searxng" && b.key == "") {
		return &Backend{name: ddgName, endpoint: ddgEndpoint, client: b.client}
	}
	return b
}

func (b *Backend) Name() string { return b.name }

// Keyed reports whether the backend is a configured provider (a key or a
// SearXNG instance) rather than the keyless DuckDuckGo fallback.
func (b *Backend) Keyed() bool { return b.name != ddgName }

// Private reports whether searches stay with the user: a SearXNG instance
// marked private.
func (b *Backend) Private() bool { return b.private }

// Describe names the backend for settings screens.
func (b *Backend) Describe() string {
	switch b.name {
	case "brave":
		return "Brave (key)"
	case "tavily":
		return "Tavily (key)"
	case "searxng":
		if b.private {
			return "SearXNG (private)"
		}
		return "SearXNG"
	}
	return "DuckDuckGo"
}

func (b *Backend) Search(ctx context.Context, query string, n int) ([]Result, error) {
	if b.name == ddgName {
		return b.searchDDG(ctx, query, n)
	}
	var req *http.Request
	var err error
	switch b.name {
	case "tavily":
		body, _ := json.Marshal(map[string]any{"query": query, "max_results": n})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+b.key)
		}
	case "brave":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"?"+url.Values{"q": {query}, "count": {fmt.Sprint(n)}}.Encode(), nil)
		if err == nil {
			req.Header.Set("X-Subscription-Token", b.key)
		}
	default:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"?"+url.Values{"q": {query}, "format": {"json"}}.Encode(), nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s search: %w", b.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("%s search: %w", b.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s search: HTTP %d: %s", b.name, resp.StatusCode, firstBytes(string(data), 200))
	}
	var parsed struct {
		Web struct {
			Results []struct{ Title, URL, Description string }
		}
		Results []struct{ Title, URL, Content string }
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("%s search: decode response: %w", b.name, err)
	}
	var out []Result
	for _, r := range parsed.Web.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	for _, r := range parsed.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out[:min(len(out), n)], nil
}

// SearchTool runs web searches through a Backend.
type SearchTool struct {
	Backend *Backend
	// MaxResults bounds the results returned.
	MaxResults int
}

// NewSearchTool returns the search tool for cfg, completed from getenv.
// Without a configured provider it searches DuckDuckGo.
func NewSearchTool(cfg SearchConfig, getenv func(string) string) *SearchTool {
	resolved := cfg.resolved(getenv)
	return &SearchTool{Backend: newBackend(resolved), MaxResults: resolved.MaxResults}
}

func (t *SearchTool) Name() string  { return SearchToolName }
func (t *SearchTool) Label() string { return "" }

func (t *SearchTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        SearchToolName,
		Description: "Search the web. Returns title, URL and snippet per result; use web_fetch to read a page.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
	}
}

func (t *SearchTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

func (t *SearchTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("web_search: invalid params: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return agent.AgentToolResult{}, errors.New("web_search: query is empty")
	}
	results, err := t.Backend.Search(ctx, p.Query, t.MaxResults)
	if errors.Is(err, ErrDDGNoResults) {
		return agent.AgentToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if len(results) == 0 {
		return agent.AgentToolResult{Content: "No results."}, nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, cleanText(r.Title), r.URL)
		if snippet := firstBytes(cleanText(r.Snippet), snippetLimit); snippet != "" {
			fmt.Fprintf(&b, "   %s\n", snippet)
		}
	}
	return agent.AgentToolResult{
		Content: strings.TrimRight(b.String(), "\n"),
		Details: map[string]any{"provider": t.Backend.Name(), "results": len(results)},
	}, nil
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// cleanText strips markup providers put in titles and snippets.
func cleanText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(s, ""))), " ")
}

func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return strings.TrimSpace(s[:n]) + "…"
}
