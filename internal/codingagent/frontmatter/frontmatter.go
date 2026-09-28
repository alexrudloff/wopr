// Package frontmatter parses YAML frontmatter blocks from markdown files.
package frontmatter

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/alexrudloff/wopr/internal/text"
)

// Doc is a parsed markdown-with-frontmatter file.
type Doc struct {
	// Frontmatter holds the YAML values from the document header.
	Frontmatter map[string]any
	// Body is the trimmed markdown content following the closing `---`.
	Body string
	// Err reports malformed YAML. The body remains available to callers that
	// intentionally tolerate malformed frontmatter.
	Err error
}

// Parse extracts and parses a YAML frontmatter block plus body. If the file
// does not begin with a closed frontmatter block, the whole input is returned
// as Body and Frontmatter is empty.
func Parse(content string) Doc {
	s := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text.StripBom(content))
	if !strings.HasPrefix(s, "---") {
		return Doc{Frontmatter: map[string]any{}, Body: s}
	}
	end := strings.Index(s[3:], "\n---")
	if end < 0 {
		return Doc{Frontmatter: map[string]any{}, Body: s}
	}
	end += 3
	body := strings.TrimSpace(s[end+4:])
	fields := map[string]any{}
	if end > 3 {
		var parsed any
		if err := yaml.Unmarshal([]byte(s[4:end]), &parsed); err != nil {
			return Doc{Frontmatter: map[string]any{}, Body: body, Err: fmt.Errorf("parse frontmatter: %w", err)}
		}
		if parsedFields, ok := parsed.(map[string]any); ok {
			fields = parsedFields
		}
	}
	return Doc{Frontmatter: fields, Body: body}
}

// String returns the named field as a string, or "" if missing.
func (d Doc) String(key string) string {
	switch t := d.Frontmatter[key].(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

// Bool returns the named field as a bool, defaulting to false.
func (d Doc) Bool(key string) bool {
	switch v := d.Frontmatter[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "yes" || v == "True" || v == "TRUE"
	}
	return false
}
