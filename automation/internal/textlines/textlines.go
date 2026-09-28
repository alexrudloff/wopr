// Package textlines splits text into lines the way Unicode defines them.
package textlines

import "unicode/utf8"

// Split splits text at every line boundary Unicode text recognizes (the set
// Python's str.splitlines uses), so line numbers do not depend on how a file
// breaks its lines. Invalid UTF-8 counts as ordinary text.
func Split[T ~string | ~[]byte](text T) []T {
	var lines []T
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(string(text[i:min(i+utf8.UTFMax, len(text))]))
		next := i + size
		switch r {
		case '\r':
			if next < len(text) && text[next] == '\n' {
				next++
			}
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		default:
			i = next
			continue
		}
		lines = append(lines, text[start:i])
		start, i = next, next
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
