package ai

import (
	"encoding/json"
	"time"
)

// DiagnosticServerWebSearch is the diagnostic type of a web search the
// provider ran on its own servers. Details holds "query" and "results", a
// list of {"title", "url"}; "error" when the search failed.
const DiagnosticServerWebSearch = "server_web_search"

// WebSearchToolName is the function tool a server search replaces.
const WebSearchToolName = "web_search"

// serverSearchMaxUses bounds the searches one Anthropic reply may run.
const serverSearchMaxUses = 5

// ServerSearchResult is one hit of a server web search.
type ServerSearchResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// addServerSearch records a server web search on the reply being built.
func (builder *assistantStreamBuilder) addServerSearch(query string, results []ServerSearchResult, errText string) {
	details := map[string]any{"query": query}
	list := make([]any, 0, len(results))
	for _, r := range results {
		list = append(list, map[string]any{"title": r.Title, "url": r.URL})
	}
	details["results"] = list
	if errText != "" {
		details["error"] = errText
	}
	builder.partial.Diagnostics = append(builder.partial.Diagnostics, AssistantMessageDiagnostic{
		Type: DiagnosticServerWebSearch, Timestamp: time.Now().UnixMilli(), Details: details,
	})
}

// parseAnthropicSearchResults reads a web_search_tool_result's content: a
// list of web_search_result blocks, or an error object.
func parseAnthropicSearchResults(raw json.RawMessage) ([]ServerSearchResult, string) {
	var results []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if json.Unmarshal(raw, &results) == nil {
		out := make([]ServerSearchResult, 0, len(results))
		for _, r := range results {
			out = append(out, ServerSearchResult{Title: r.Title, URL: r.URL})
		}
		return out, ""
	}
	var failure struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(raw, &failure)
	if failure.ErrorCode == "" {
		failure.ErrorCode = "unknown error"
	}
	return nil, failure.ErrorCode
}

// parseResponsesSearchCall reads a Responses web_search_call item: its query
// and, when sources were included, the pages it found.
func parseResponsesSearchCall(raw json.RawMessage) (string, []ServerSearchResult) {
	var item struct {
		Action struct {
			Query   string `json:"query"`
			Sources []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"sources"`
		} `json:"action"`
	}
	_ = json.Unmarshal(raw, &item)
	out := make([]ServerSearchResult, 0, len(item.Action.Sources))
	for _, s := range item.Action.Sources {
		out = append(out, ServerSearchResult{Title: s.Title, URL: s.URL})
	}
	return item.Action.Query, out
}

// addResponsesCitations fills titles, or the results themselves, of the
// reply's server searches from a message's url_citation annotations.
func addResponsesCitations(builder *assistantStreamBuilder, parts []respOutputContent) {
	diagnostics := builder.partial.Diagnostics
	last := -1
	for i, d := range diagnostics {
		if d.Type == DiagnosticServerWebSearch {
			last = i
		}
	}
	if last < 0 {
		return
	}
	results, _ := diagnostics[last].Details["results"].([]any)
	seen := map[string]int{}
	for i, r := range results {
		if m, ok := r.(map[string]any); ok {
			url, _ := m["url"].(string)
			seen[url] = i
		}
	}
	for _, part := range parts {
		for _, a := range part.Annotations {
			if a.Type != "url_citation" || a.URL == "" {
				continue
			}
			if i, ok := seen[a.URL]; ok {
				if m, ok := results[i].(map[string]any); ok && m["title"] == "" {
					m["title"] = a.Title
				}
				continue
			}
			seen[a.URL] = len(results)
			results = append(results, map[string]any{"title": a.Title, "url": a.URL})
		}
	}
	diagnostics[last].Details["results"] = results
}
