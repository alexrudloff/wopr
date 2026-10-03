package sessionblob

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// LineLimit is the most a session line may hold, not counting image data
// (which Externalize moves to the blob store). FieldLimit is the smallest
// string Shrink will omit to get there.
const (
	LineLimit  = 1 << 20
	FieldLimit = 64 << 10
)

// Shrink keeps one session line under LineLimit by replacing the largest
// strings in its non-model-facing parts (any "details" object, and a
// "custom" entry's "data") with a short "[omitted from the session: 8.4 MB]"
// note, largest first, until the line fits. What the model saw (message
// content, summaries, custom message content) is never changed, and image
// data is left for Externalize. It returns the line, how many strings it
// omitted, and whether the line is still over the limit, which only
// model-facing content can cause. A line under the limit, or one that
// doesn't parse, is returned as is.
func Shrink(line []byte) (out []byte, omitted int, over bool) {
	if len(line) <= LineLimit {
		return line, 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var root map[string]any
	if dec.Decode(&root) != nil {
		return line, 0, false
	}
	type field struct {
		set  func(string)
		size int
	}
	var fields []field
	imageBytes := 0
	var walk func(v any, collect bool)
	walk = func(v any, collect bool) {
		switch t := v.(type) {
		case map[string]any:
			if t["type"] == "image" {
				if d, ok := t["data"].(string); ok {
					imageBytes += len(d)
				}
				return
			}
			for k, child := range t {
				c := collect || k == "details"
				if s, ok := child.(string); ok && c && len(s) >= FieldLimit {
					m, k := t, k
					fields = append(fields, field{size: len(s), set: func(note string) { m[k] = note }})
					continue
				}
				walk(child, c)
			}
		case []any:
			for i, child := range t {
				if s, ok := child.(string); ok && collect && len(s) >= FieldLimit {
					a, i := t, i
					fields = append(fields, field{size: len(s), set: func(note string) { a[i] = note }})
					continue
				}
				walk(child, collect)
			}
		}
	}
	for k, child := range root {
		c := k == "details" || (root["type"] == "custom" && k == "data")
		if s, ok := child.(string); ok && c && len(s) >= FieldLimit {
			k := k
			fields = append(fields, field{size: len(s), set: func(note string) { root[k] = note }})
			continue
		}
		walk(child, c)
	}
	size := len(line) - imageBytes
	if size <= LineLimit {
		return line, 0, false
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].size > fields[j].size })
	for _, f := range fields {
		if size <= LineLimit {
			break
		}
		note := fmt.Sprintf("[omitted from the session: %.1f MB]", float64(f.size)/(1<<20))
		f.set(note)
		size -= f.size - len(note)
		omitted++
	}
	if omitted == 0 {
		return line, 0, true
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(root) != nil {
		return line, 0, true
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), omitted, size > LineLimit
}
