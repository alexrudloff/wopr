# Web tools

WOPR has two web tools, `web_fetch` and `web_search`. Both are on by default and need no setup.

## web_fetch

`web_fetch` downloads an http or https URL and returns readable text: HTML becomes markdown with navigation, scripts, forms and hidden elements removed; PDFs return their text page by page; plain text, JSON and XML pass through. Images and other binary types are reported as unsupported.

A page longer than 16 KB keeps an 8 KB head in context. The full text goes to the observation archive, and the result ends with the `obs_recall` call that continues where the head stops.

By default `web_fetch` refuses loopback, private, link-local and CGNAT addresses, checked after DNS resolution and on every redirect, and never uses a proxy. `file://` and other schemes are refused.

## web_search

`web_search` returns the title, URL and a short snippet for each result. Each search goes, in order, to:

1. **A search provider you configured**: a Brave or Tavily key, or a SearXNG instance. It is used for every model.
2. **The model provider's own search**, when none is configured and the request is served by Claude (Anthropic) or a GPT/Codex model (OpenAI). The provider runs the search on its servers; the conversation shows it as a `web_search` card with the query and the pages found. If a provider refuses it, that provider uses DuckDuckGo for the rest of the session.
3. **DuckDuckGo**, for every other model: wopr reads DuckDuckGo's results page, with no key. When DuckDuckGo returns nothing, which usually means it is rate-limiting, the result says so rather than coming back empty.

Set a provider in `/setup` → **Web search** (keys are stored in `auth.json`), by environment variable, or in settings:

| Backend | Environment variable | Settings |
|---|---|---|
| Brave Search | `BRAVE_API_KEY` | `"search": {"provider": "brave", "apiKey": "..."}` |
| Tavily | `TAVILY_API_KEY` | `"search": {"provider": "tavily", "apiKey": "..."}` |
| SearXNG | `SEARXNG_URL` | `"search": {"provider": "searxng", "url": "http://localhost:8888"}` |

Brave and Tavily keys go only to their own endpoints. A SearXNG instance must have its JSON output format enabled. Mark a SearXNG instance you run as private (`"private": true`, or **Private SearXNG** in `/setup`) and private mode counts its searches as private; every other search still runs in private mode, with a note that the query left your machine.

## Settings

```json
{
  "web": {
    "allowPrivateNetwork": false,
    "fetchTimeoutMs": 30000,
    "maxBytes": 5242880,
    "search": { "provider": "brave", "maxResults": 5 }
  }
}
```

`allowPrivateNetwork` lets `web_fetch` reach local and private addresses and use the environment's proxy. `maxBytes` caps the download. To turn a web tool off, leave it out of `defaultTools` or pass `--exclude-tools web_fetch`.
