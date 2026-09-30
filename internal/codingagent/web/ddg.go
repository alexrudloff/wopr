package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// DuckDuckGo search without a key: the results page of html.duckduckgo.com,
// read like a browser would. It can rate-limit or answer with a challenge
// page; both come back as ErrDDGNoResults rather than an empty list, so the
// model never mistakes a block for "nothing exists".

const (
	ddgEndpoint  = "https://html.duckduckgo.com/html/"
	ddgUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
)

// ErrDDGNoResults is what a DuckDuckGo search without results reports.
var ErrDDGNoResults = fmt.Errorf("DuckDuckGo returned no results (possibly rate-limited); try different terms or web_fetch a known URL")

// ddgSlots and ddgSpacing pace searches across the process: a war council
// searches from several members at once, and DuckDuckGo blocks bursts.
var (
	ddgSlots   = make(chan struct{}, 2)
	ddgMu      sync.Mutex
	ddgNext    time.Time
	ddgSpacing = 750 * time.Millisecond
)

// ddgTurn waits for a free slot and the spacing since the last search
// started; release frees the slot.
func ddgTurn(ctx context.Context) (release func(), err error) {
	select {
	case ddgSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ddgMu.Lock()
	wait := time.Until(ddgNext)
	ddgNext = time.Now().Add(max(wait, 0) + ddgSpacing)
	ddgMu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			<-ddgSlots
			return nil, ctx.Err()
		}
	}
	return func() { <-ddgSlots }, nil
}

func (b *Backend) searchDDG(ctx context.Context, query string, n int) ([]Result, error) {
	release, err := ddgTurn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"?"+url.Values{"q": {query}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ddgUserAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("duckduckgo search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo search: HTTP %d; %w", resp.StatusCode, ErrDDGNoResults)
	}
	results, err := parseDDG(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, ErrDDGNoResults
	}
	return results[:min(len(results), n)], nil
}

// parseDDG reads the organic results of a DuckDuckGo HTML results page,
// skipping ads.
func parseDDG(r io.Reader) ([]Result, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo search: %w", err)
	}
	var out []Result
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, "result") && !hasClass(n, "result--ad") {
			var res Result
			if a := find(n, func(c *html.Node) bool { return hasClass(c, "result__a") }); a != nil {
				res.Title, res.URL = collapse(textOf(a)), ddgTarget(attr(a, "href"))
			}
			if s := find(n, func(c *html.Node) bool { return hasClass(c, "result__snippet") }); s != nil {
				res.Snippet = collapse(textOf(s))
			}
			if res.URL != "" && res.Title != "" {
				out = append(out, res)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out, nil
}

// ddgTarget unwraps DuckDuckGo's /l/?uddg= redirect links.
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Host, "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return href
}

func hasClass(n *html.Node, class string) bool {
	return n.Type == html.ElementNode && strings.Contains(" "+attr(n, "class")+" ", " "+class+" ")
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
