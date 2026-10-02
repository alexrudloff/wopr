package tui

import (
	"regexp"
	"strings"
)

// bareURL matches an http(s) URL in plain text.
var bareURL = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

// LinkifyURLs wraps the bare http(s) URLs in plain text in OSC 8
// hyperlinks, when the terminal supports them. Trailing punctuation stays
// outside the link, as in markdown autolinks.
func LinkifyURLs(text string) string {
	if !Capabilities().Hyperlinks || !strings.Contains(text, "://") {
		return text
	}
	return bareURL.ReplaceAllStringFunc(text, func(match string) string {
		url := trimTrailingLinkPunctuation(match)
		return Hyperlink(url, url) + match[len(url):]
	})
}
