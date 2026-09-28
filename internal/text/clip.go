package text

import "unicode/utf8"

// Clip cuts s to at most n bytes without splitting a UTF-8 character and
// marks the cut with "…". It returns s unchanged when s fits.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
