package codingagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Setup edits models.json and router.json in place: it finds a value by its
// key path and inserts or replaces text there, so everything it does not
// touch (comments, key order, formatting) stays exactly as the user wrote
// it. The scanner understands JSON plus // and /* */ comments and trailing
// commas, the dialect models.json accepts.

// jsoncSpan is a value's byte range in the source; entry is where its
// whole entry starts (the key, for an object member).
type jsoncSpan struct{ start, end, entry int }

// jsoncScanner walks one source text.
type jsoncScanner struct{ src string }

// skip returns the offset of the next token at or after i.
func (s jsoncScanner) skip(i int) int {
	for i < len(s.src) {
		switch c := s.src[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(s.src[i:], "//"):
			for i < len(s.src) && s.src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s.src[i:], "/*"):
			end := strings.Index(s.src[i+2:], "*/")
			if end < 0 {
				return len(s.src)
			}
			i += end + 4
		default:
			return i
		}
	}
	return i
}

var errJSONC = errors.New("malformed JSON")

// value returns the end of the value starting at i.
func (s jsoncScanner) value(i int) (int, error) {
	i = s.skip(i)
	if i >= len(s.src) {
		return 0, errJSONC
	}
	switch s.src[i] {
	case '"':
		return s.str(i)
	case '{', '[':
		closer := byte('}')
		if s.src[i] == '[' {
			closer = ']'
		}
		i = s.skip(i + 1)
		for i < len(s.src) && s.src[i] != closer {
			if closer == '}' {
				end, err := s.str(i)
				if err != nil {
					return 0, err
				}
				i = s.skip(end)
				if i >= len(s.src) || s.src[i] != ':' {
					return 0, errJSONC
				}
				i++
			}
			end, err := s.value(i)
			if err != nil {
				return 0, err
			}
			i = s.skip(end)
			if i < len(s.src) && s.src[i] == ',' {
				i = s.skip(i + 1)
			}
		}
		if i >= len(s.src) {
			return 0, errJSONC
		}
		return i + 1, nil
	}
	// A literal: number, true, false, or null.
	for i < len(s.src) && !strings.ContainsRune(" \t\r\n,]}/", rune(s.src[i])) {
		i++
	}
	return i, nil
}

// str returns the end of the string starting at i.
func (s jsoncScanner) str(i int) (int, error) {
	if i >= len(s.src) || s.src[i] != '"' {
		return 0, errJSONC
	}
	for i++; i < len(s.src); i++ {
		switch s.src[i] {
		case '\\':
			i++
		case '"':
			return i + 1, nil
		}
	}
	return 0, errJSONC
}

// member finds key's value in the object spanning obj.
func (s jsoncScanner) member(obj jsoncSpan, key string) (jsoncSpan, bool, error) {
	i := s.skip(obj.start)
	if i >= len(s.src) || s.src[i] != '{' {
		return jsoncSpan{}, false, errJSONC
	}
	for i = s.skip(i + 1); i < obj.end-1; {
		keyStart := i
		end, err := s.str(i)
		if err != nil {
			return jsoncSpan{}, false, err
		}
		name, err := strconv.Unquote(s.src[i:end])
		if err != nil {
			return jsoncSpan{}, false, err
		}
		i = s.skip(end) + 1 // past ':'
		start := s.skip(i)
		vend, err := s.value(start)
		if err != nil {
			return jsoncSpan{}, false, err
		}
		if name == key {
			return jsoncSpan{start, vend, keyStart}, true, nil
		}
		i = s.skip(vend)
		if i < len(s.src) && s.src[i] == ',' {
			i = s.skip(i + 1)
		}
	}
	return jsoncSpan{}, false, nil
}

// element finds the n-th element of the array spanning arr.
func (s jsoncScanner) element(arr jsoncSpan, n int) (jsoncSpan, bool, error) {
	i := s.skip(arr.start)
	if i >= len(s.src) || s.src[i] != '[' {
		return jsoncSpan{}, false, errJSONC
	}
	for index, i := 0, s.skip(i+1); i < arr.end-1; index++ {
		end, err := s.value(i)
		if err != nil {
			return jsoncSpan{}, false, err
		}
		if index == n {
			return jsoncSpan{i, end, i}, true, nil
		}
		i = s.skip(end)
		if i < len(s.src) && s.src[i] == ',' {
			i = s.skip(i + 1)
		}
	}
	return jsoncSpan{}, false, nil
}

// jsoncFind returns the span of the value at path, where a path element is
// an object key or "#n" for an array index. The empty path is the root.
func jsoncFind(src string, path ...string) (jsoncSpan, bool, error) {
	s := jsoncScanner{src}
	start := s.skip(0)
	end, err := s.value(start)
	if err != nil {
		return jsoncSpan{}, false, err
	}
	span := jsoncSpan{start, end, start}
	for _, key := range path {
		var ok bool
		if index, isIndex := strings.CutPrefix(key, "#"); isIndex {
			n, convErr := strconv.Atoi(index)
			if convErr != nil {
				return jsoncSpan{}, false, convErr
			}
			span, ok, err = s.element(span, n)
		} else {
			span, ok, err = s.member(span, key)
		}
		if err != nil || !ok {
			return jsoncSpan{}, false, err
		}
	}
	return span, true, nil
}

// jsoncReplace swaps the value at span for text.
func jsoncReplace(src string, span jsoncSpan, text string) string {
	return src[:span.start] + reindent(text, lineIndent(src, span.start)) + src[span.end:]
}

// jsoncAppend adds item (a "key": value member for an object, a value for
// an array) as the last entry of the container at span, one level deeper
// than the container's closing line, with a comma after the previous entry.
func jsoncAppend(src string, span jsoncSpan, item string) string {
	s := jsoncScanner{src}
	closer := span.end - 1
	isObject := src[span.start] == '{'
	// last is the end of the last entry (past its comma, if any), or the
	// opening bracket when the container is empty.
	last := span.start + 1
	empty := true
	for i := s.skip(span.start + 1); i < closer; {
		start := i
		if isObject {
			keyEnd, err := s.str(i)
			if err != nil {
				break
			}
			start = s.skip(keyEnd) + 1
		}
		end, err := s.value(start)
		if err != nil {
			break
		}
		last, empty = end, false
		i = s.skip(end)
		if i < closer && src[i] == ',' {
			last = i + 1
			i = s.skip(i + 1)
		}
	}
	sep := ""
	if !empty && src[last-1] != ',' {
		sep = ","
	}
	// A comment on the rest of the last entry's line stays with it.
	insertAt := last
	if nl := strings.IndexByte(src[last:closer], '\n'); nl >= 0 && strings.HasPrefix(strings.TrimLeft(src[last:last+nl], " \t"), "//") {
		insertAt = last + nl
	}
	outer := lineIndent(src, closer)
	inner := outer + "  "
	var b strings.Builder
	b.WriteString(src[:last] + sep + src[last:insertAt])
	b.WriteString("\n" + inner + reindent(item, inner))
	if rest := strings.TrimSpace(src[insertAt:closer]); rest != "" {
		// Comments between the last entry and the closer.
		b.WriteString("\n" + inner + reindent(rest, inner))
	}
	b.WriteString("\n" + outer + src[closer:])
	return b.String()
}

// lineIndent is the leading whitespace of the line holding offset.
func lineIndent(src string, offset int) string {
	start := strings.LastIndexByte(src[:offset], '\n') + 1
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return src[start:end]
}

// reindent prefixes every line of text but the first with indent.
func reindent(text, indent string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n"+indent)
}

// jsoncMember renders a "key": value member.
func jsoncMember(key, value string) string {
	return fmt.Sprintf("%q: %s", key, value)
}

// compactJSON indents v like json.MarshalIndent but keeps small flat
// objects and arrays on one line, as people write config by hand:
// "cost": { "input": 0, "output": 0 }.
func compactJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		opener := strings.HasSuffix(line, "{") || strings.HasSuffix(line, "[")
		if !opener {
			out = append(out, line)
			continue
		}
		indent := lineIndent(line, 0)
		var inner []string
		j := i + 1
		flat := true
		for ; j < len(lines); j++ {
			trimmed := strings.TrimSpace(lines[j])
			if lineIndent(lines[j], 0) == indent && (strings.HasPrefix(trimmed, "}") || strings.HasPrefix(trimmed, "]")) {
				break
			}
			if strings.HasSuffix(trimmed, "{") || strings.HasSuffix(trimmed, "[") {
				flat = false
				break
			}
			inner = append(inner, strings.TrimSuffix(trimmed, ","))
		}
		if !flat || j >= len(lines) || len(inner) == 0 {
			out = append(out, line)
			continue
		}
		closer := strings.TrimSpace(lines[j])
		joined := line + " " + strings.Join(inner, ", ") + " " + closer
		if len(joined) > 100 {
			out = append(out, line)
			continue
		}
		out = append(out, joined)
		i = j
	}
	return strings.Join(out, "\n"), nil
}

// jsoncRemove deletes the entry at span (a member or an element) with its
// comma, and its line when the entry had the line to itself.
func jsoncRemove(src string, span jsoncSpan) string {
	s := jsoncScanner{src}
	start, end := span.entry, span.end
	if next := s.skip(end); next < len(src) && src[next] == ',' {
		end = next + 1
	} else {
		// The last entry: drop the comma before it instead.
		k := start - 1
		for k >= 0 && strings.ContainsRune(" \t\r\n", rune(src[k])) {
			k--
		}
		if k >= 0 && src[k] == ',' {
			return src[:k] + src[span.end:]
		}
	}
	lineStart := strings.LastIndexByte(src[:start], '\n') + 1
	if strings.TrimSpace(src[lineStart:start]) == "" {
		lineEnd := end
		for lineEnd < len(src) && (src[lineEnd] == ' ' || src[lineEnd] == '\t') {
			lineEnd++
		}
		if lineEnd < len(src) && src[lineEnd] == '\n' {
			start, end = lineStart, lineEnd+1
		}
	}
	return src[:start] + src[end:]
}
