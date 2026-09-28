package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Archive stores a full tool result where obs_recall can page it and returns
// the observation id. name labels the source and key distinguishes records.
type Archive func(name, key, text string) (string, error)

// ClipForContext keeps text that fits in limit bytes. Longer text is cut to a
// head of about head bytes, ending on a line break, and a footer says how to
// read the rest: an obs_recall call starting at the cut when archive stores
// the full text, otherwise a plain truncation notice.
func ClipForContext(text string, limit, head int, archive Archive, name, key string) string {
	if len(text) <= limit {
		return text
	}
	cut := min(head, len(text))
	if i := strings.LastIndexByte(text[:cut], '\n'); i > cut/2 {
		cut = i + 1
	}
	for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut--
	}
	shown := text[:cut]
	if archive != nil {
		if id, err := archive(name, key, text); err == nil {
			return fmt.Sprintf("%s\n[showing %d of %d bytes (~%d tokens). Rest: obs_recall {\"id\":%q,\"offset\":%d}]", strings.TrimRight(shown, "\n"), cut, len(text), len(text)/4, id, cut)
		}
	}
	return fmt.Sprintf("%s\n[truncated: showing %d of %d bytes]", strings.TrimRight(shown, "\n"), cut, len(text))
}
