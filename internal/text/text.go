// Package text provides the leading byte order mark helpers every reader
// applies before parsing user-authored JSON, markdown, and text files, and the
// JavaScript whitespace set that ported JS parsers match byte-for-byte.
package text

import "strings"

// bom is the UTF-8 byte order mark as decoded text (U+FEFF).
const bom = "\xef\xbb\xbf"

// SplitBom splits a leading UTF-8 byte order mark from decoded text. It
// returns the mark ("" when absent) and the remaining text.
func SplitBom(content string) (mark, text string) {
	if strings.HasPrefix(content, bom) {
		return bom, content[len(bom):]
	}
	return "", content
}

// StripBom removes a leading UTF-8 byte order mark from decoded text.
func StripBom(content string) string {
	_, text := SplitBom(content)
	return text
}

// StripBomBytes is StripBom for file contents read as bytes, so JSON readers
// can strip the mark before decoding without a string round trip.
func StripBomBytes(content []byte) []byte {
	if len(content) >= len(bom) && string(content[:len(bom)]) == bom {
		return content[len(bom):]
	}
	return content
}

// IsJSSpace reports the JavaScript regex \s / String.prototype.trim whitespace
// set (WhiteSpace + LineTerminator).
func IsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		'\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// TrimJS, TrimStartJS and TrimEndJS mirror String.prototype.trim,
// trimStart and trimEnd.
func TrimJS(s string) string      { return strings.TrimFunc(s, IsJSSpace) }
func TrimStartJS(s string) string { return strings.TrimLeftFunc(s, IsJSSpace) }
func TrimEndJS(s string) string   { return strings.TrimRightFunc(s, IsJSSpace) }
