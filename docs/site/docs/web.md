# Web tools

WOPR has two web tools. `web_fetch` is on by default. `web_search` appears only when a search backend is configured, so it costs nothing otherwise.

## web_fetch

`web_fetch` downloads an http or https URL and returns readable text: HTML becomes markdown with navigation, scripts, forms and hidden elements removed; PDFs return their text page by page; plain text, JSON and XML pass through. Images and other binary types are reported as unsupported.

A page longer than 16 KB keeps an 8 KB head in context. The full text goes to the observation archive, and the result ends with the `obs_recall` call that continues where the head stops.

By default `web_fetch` refuses loopback, private, link-local and CGNAT addresses, checked after DNS resolution and on every redirect, and never uses a proxy. `file://` and other schemes are refused.

## web_search

`web_search` returns the title, URL and a short snippet for each result. Configure one backend, either by environment variable or in settings:

| Backend | Environment variable | Settings |
|---|---|---|
| Brave Search | `BRAVE_API_KEY` | `"search": {"provider": "brave", "apiKey": "..."}` |
| Tavily | `TAVILY_API_KEY` | `"search": {"provider": "tavily", "apiKey": "..."}` |
| SearXNG | `SEARXNG_URL` | `"search": {"provider": "searxng", "url": "http://localhost:8888"}` |

Brave and Tavily keys go only to their own endpoints. A SearXNG instance must have its JSON output format enabled.

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
