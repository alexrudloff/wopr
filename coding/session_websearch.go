package coding

import (
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
)

// Web search picks, in order: a configured provider (a Brave or Tavily key,
// or a SearXNG instance), else the search the serving model's provider runs
// on its own servers (Anthropic, OpenAI), else DuckDuckGo's results page. A
// provider that refuses its server search falls back to DuckDuckGo for the
// rest of the session.

// serverSearchProviders run web search on their own servers.
var serverSearchProviders = map[string]bool{"anthropic": true, "openai": true, "openai-codex": true}

// webSearchState is the session's search backend and which providers
// refused their server search.
type webSearchState struct {
	tool *web.SearchTool
	off  sync.Map // provider id → true
}

// storedKey reads a key saved in auth.json, or "".
func storedKey(auth *ai.AuthStorage) func(string) string {
	return func(id string) string {
		if auth == nil {
			return ""
		}
		cred, _, _ := auth.Get(id)
		return cred.Key
	}
}

// useServerSearch reports whether a request to model declares the
// provider's server search in place of web_search.
func (s *Session) useServerSearch(model *ai.Model, transcript ai.TranscriptContext) bool {
	if s.webSearch.tool == nil || s.webSearch.tool.Backend.Keyed() || model == nil {
		return false
	}
	provider := providerID(model)
	if !serverSearchProviders[provider] {
		return false
	}
	if off, _ := s.webSearch.off.Load(provider); off == true {
		return false
	}
	// A conversation that already called the web_search function keeps it:
	// a server tool of the same name would clash with those calls.
	for _, message := range transcript.Messages() {
		if assistant, ok := message.(ai.AssistantMessage); ok {
			for _, block := range assistant.Content {
				if call, ok := block.(ai.ToolCall); ok && call.Name == web.SearchToolName {
					return false
				}
			}
		}
	}
	return true
}

// serverSearchRefused turns server search off for a provider whose request
// failed over it, and reports whether it did, so the request can be retried
// with DuckDuckGo.
func (s *Session) serverSearchRefused(message *agent.AssistantMessage) bool {
	if message == nil || message.StopReason != ai.StopReasonError || !serverSearchProviders[message.Provider] {
		return false
	}
	text := strings.ToLower(message.ErrorMessage)
	if !strings.Contains(text, "web_search") {
		return false
	}
	if off, _ := s.webSearch.off.Load(message.Provider); off == true {
		return false
	}
	s.webSearch.off.Store(message.Provider, true)
	return true
}
