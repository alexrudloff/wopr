package main

import (
	"fmt"
	"strings"
)

// The JSON-LD output keeps its key order and its escaping stable, so the
// published file changes only when the graph does: keys stay in the order
// written here, every non-ASCII character is escaped as \uXXXX, and nesting
// is indented by two spaces.

// field is one key of an ordered JSON object.
type field struct {
	key   string
	value any // string, []field, or []any
}

func jsonLD(g graph) string {
	nodes := make([]any, 0, len(g.Entities)+len(g.Relations))
	for _, e := range g.Entities {
		nodes = append(nodes, []field{
			{"@id", "wopr:" + e.ID},
			{"@type", "DefinedTerm"},
			{"name", e.Label},
			{"description", e.Summary},
			{"wopr:kind", e.Kind},
			{"wopr:where", e.Where},
			{"wopr:inspect", e.Inspect},
		})
	}
	for _, r := range g.Relations {
		nodes = append(nodes, []field{
			{"@type", "wopr:Relation"},
			{"wopr:from", "wopr:" + r[0]},
			{"wopr:relation", r[1]},
			{"wopr:to", "wopr:" + r[2]},
			{"description", r[3]},
		})
	}
	doc := []field{
		{"@context", []field{
			{"@vocab", "https://schema.org/"},
			{"wopr", "https://github.com/alexrudloff/wopr/terms#"},
			{"relations", "wopr:relation"},
		}},
		{"@graph", nodes},
	}
	var b strings.Builder
	writeJSON(&b, doc, 0)
	b.WriteString("\n")
	return b.String()
}

func writeJSON(b *strings.Builder, value any, depth int) {
	indent := strings.Repeat("  ", depth+1)
	closing := strings.Repeat("  ", depth)
	switch v := value.(type) {
	case string:
		writeString(b, v)
	case []field:
		if len(v) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, f := range v {
			b.WriteString(indent)
			writeString(b, f.key)
			b.WriteString(": ")
			writeJSON(b, f.value, depth+1)
			if i < len(v)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(closing + "}")
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range v {
			b.WriteString(indent)
			writeJSON(b, item, depth+1)
			if i < len(v)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(closing + "]")
	default:
		panic(fmt.Sprintf("jsonld: unsupported value %T", value))
	}
}

// writeString quotes s as ASCII-only JSON.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			fmt.Fprintf(b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
}
