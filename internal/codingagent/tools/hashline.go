package tools

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Hashline: read prefixes each text line with an anchor, the line number
// plus two letters hashed from the line and the one before it ("12ab|").
// edit accepts anchors in place of oldText, so a weak model never has to
// reproduce text exactly, and an anchor whose line changed since the read is
// rejected instead of guessed. The mode is per model: HashlineMode decides it
// for the model serving a tool call, and HashlineSchema is the declaration
// that model sees.

// HashlineMode reports whether hashline anchors are on for provider/model.
type HashlineMode func(provider, model string) bool

// hashlineOn reports the mode for the model serving the tool call in ctx.
func hashlineOn(ctx context.Context, mode HashlineMode) bool {
	if mode == nil {
		return false
	}
	env, _ := agent.ToolEnvironmentFrom(ctx)
	return mode(env.Provider, env.Model)
}

// lineHash is the two-letter hash of line, chained to prev so a line that
// merely moved next to the same text elsewhere still reads as changed.
func lineHash(prev, line string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSuffix(prev, "\r")))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSuffix(line, "\r")))
	v := h.Sum32()
	return string([]byte{'a' + byte(v%26), 'a' + byte(v/26%26)})
}

// lineAnchor is the anchor of lines[i] (0-based).
func lineAnchor(lines []string, i int) string {
	prev := ""
	if i > 0 {
		prev = lines[i-1]
	}
	return strconv.Itoa(i+1) + lineHash(prev, lines[i])
}

// formatHashlines renders lines[start:end] as anchored rows.
func formatHashlines(lines []string, start, end int) string {
	var b strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte('\n')
		}
		b.WriteString(lineAnchor(lines, i))
		b.WriteByte('|')
		b.WriteString(lines[i])
	}
	return b.String()
}

// hashlineLines splits LF-normalized text into lines, dropping the empty
// entry after a final newline: it is not a line a model can anchor.
func hashlineLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

var anchorPattern = regexp.MustCompile(`^\s*(\d+):?([a-z]{2})(?:\||\s|$)`)

// parseAnchor reads "12ab", "12:ab", or a pasted "12ab|text" row.
func parseAnchor(s string) (line int, hash string, ok bool) {
	m := anchorPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, m[2], true
}

// splitAnchorRange splits "12ab-15cd" or "12ab..15cd" into its two anchors.
func splitAnchorRange(s string) (string, string) {
	s = strings.TrimSpace(s)
	for _, sep := range []string{"..", "-", ","} {
		if first, last, ok := strings.Cut(s, sep); ok && anchorPattern.MatchString(last+" ") {
			return first, last
		}
	}
	return s, ""
}

var pastedAnchorPattern = regexp.MustCompile(`^\d+:?[a-z]{2}\|`)

// stripPastedAnchors removes anchor prefixes a model copied into newText,
// but only when every non-empty line carries one.
func stripPastedAnchors(text string) string {
	lines := strings.Split(text, "\n")
	seen := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		if !pastedAnchorPattern.MatchString(line) {
			return text
		}
		seen = true
	}
	if !seen {
		return text
	}
	for i, line := range lines {
		lines[i] = pastedAnchorPattern.ReplaceAllString(line, "")
	}
	return strings.Join(lines, "\n")
}

// anchoredEdit is one edit resolved to 0-based inclusive lines.
type anchoredEdit struct {
	index      int
	start, end int
	lines      []string // replacement; nil deletes
}

// staleAnchorError explains a rejected anchor and shows fresh anchors
// around the line it named.
func staleAnchorError(path, anchor string, lines []string, line int) error {
	var b strings.Builder
	if line > len(lines) {
		fmt.Fprintf(&b, "Anchor %s is past the end of %s (%d lines).", anchor, path, len(lines))
		line = len(lines)
	} else {
		fmt.Fprintf(&b, "Anchor %s is stale: line %d of %s changed since you read it.", anchor, line, path)
	}
	start, end := max(line-4, 0), min(line+3, len(lines))
	if start < end {
		b.WriteString(" Fresh anchors:\n")
		b.WriteString(formatHashlines(lines, start, end))
	}
	return fmt.Errorf("%s", b.String())
}

// resolveAnchor returns the 0-based line an anchor names, or a stale error.
func resolveAnchor(path, anchor string, lines []string) (int, error) {
	n, hash, ok := parseAnchor(anchor)
	if !ok {
		return 0, fmt.Errorf("Invalid anchor %q in %s. Use the LINE+2-letter prefix from read, e.g. 12ab.", anchor, path)
	}
	if n > len(lines) || lineAnchor(lines, n-1) != strconv.Itoa(n)+hash {
		return 0, staleAnchorError(path, strconv.Itoa(n)+hash, lines, n)
	}
	return n - 1, nil
}

// resolveAnchoredEdits checks every anchored edit against lines and returns
// them sorted by position.
func resolveAnchoredEdits(path string, lines []string, edits []editInput) ([]anchoredEdit, error) {
	out := make([]anchoredEdit, 0, len(edits))
	for i, e := range edits {
		first, last := splitAnchorRange(e.Anchor)
		if strings.TrimSpace(e.End) != "" {
			last = e.End
		}
		start, err := resolveAnchor(path, first, lines)
		if err != nil {
			return nil, err
		}
		end := start
		if strings.TrimSpace(last) != "" {
			if end, err = resolveAnchor(path, last, lines); err != nil {
				return nil, err
			}
		}
		if end < start {
			return nil, fmt.Errorf("edits[%d] in %s: end anchor %s is before anchor %s.", i, path, strings.TrimSpace(last), strings.TrimSpace(first))
		}
		var replacement []string
		if text := stripPastedAnchors(normalizeToLF(e.NewText)); text != "" {
			replacement = hashlineLines(text)
		}
		out = append(out, anchoredEdit{index: i, start: start, end: end, lines: replacement})
	}
	slices.SortStableFunc(out, func(a, b anchoredEdit) int { return cmp.Compare(a.start, b.start) })
	for i := 1; i < len(out); i++ {
		if out[i].start <= out[i-1].end {
			return nil, fmt.Errorf("edits[%d] and edits[%d] overlap in %s. Merge them into one edit.", out[i-1].index, out[i].index, path)
		}
	}
	return out, nil
}

// hashlineResult is an applied anchored edit set.
type hashlineResult struct {
	newContent string
	fresh      string // anchored rows of the changed regions
}

// maxFreshAnchorLines caps the anchors an edit result repeats.
const maxFreshAnchorLines = 20

// applyAnchoredEdits applies anchored edits to LF-normalized content and
// reports fresh anchors for each changed region plus the line after it,
// whose chained hash changed too.
func applyAnchoredEdits(normalized string, edits []editInput, path string) (hashlineResult, error) {
	trailingNewline := strings.HasSuffix(normalized, "\n")
	lines := hashlineLines(normalized)
	resolved, err := resolveAnchoredEdits(path, lines, edits)
	if err != nil {
		return hashlineResult{}, err
	}
	var out []string
	type region struct{ start, end int }
	var regions []region
	cursor := 0
	for _, e := range resolved {
		out = append(out, lines[cursor:e.start]...)
		start := len(out)
		out = append(out, e.lines...)
		regions = append(regions, region{start, len(out)})
		cursor = e.end + 1
	}
	out = append(out, lines[cursor:]...)
	newContent := strings.Join(out, "\n")
	if trailingNewline && len(out) > 0 {
		newContent += "\n"
	}
	if newContent == normalized {
		return hashlineResult{}, noChangeErr(path, len(edits))
	}
	var b strings.Builder
	shown := 0
	for _, r := range regions {
		end := min(r.end+1, len(out))
		for i := r.start; i < end; i++ {
			if shown == maxFreshAnchorLines {
				fmt.Fprintf(&b, "\n[more changed lines; read %s for their anchors]", path)
				return hashlineResult{newContent: newContent, fresh: b.String()}, nil
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(lineAnchor(out, i))
			b.WriteByte('|')
			b.WriteString(out[i])
			shown++
		}
	}
	return hashlineResult{newContent: newContent, fresh: b.String()}, nil
}

// Hashline declarations. They replace the read and edit declarations for a
// model in hashline mode and are kept short: every token is paid on every
// request.
const (
	hashlineReadNote        = " Text lines are prefixed LINEab| (an anchor: line number + 2-letter hash); edit takes the anchor, not the prefix text."
	hashlineEditDescription = "Edit one file. Prefer anchors from read: lines anchor..end (inclusive) become newText (\"\" deletes). Or match exact oldText. Stale anchors are rejected with fresh ones. Edits must not overlap."
)

// HashlineSchema returns the declaration a model in hashline mode sees for
// a read or edit declaration; other tools are returned unchanged. Extra
// properties a wrapper added (then_run) are kept.
func HashlineSchema(schema ai.ToolSchema) ai.ToolSchema {
	switch schema.Name {
	case "read":
		schema.Description += hashlineReadNote
	case "edit":
		schema.Description = hashlineEditDescription
		params := maps.Clone(schema.Parameters)
		properties := map[string]any{}
		if existing, ok := params["properties"].(map[string]any); ok {
			maps.Copy(properties, existing)
		}
		properties["edits"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"anchor":  map[string]any{"type": "string", "description": "First line's anchor, e.g. 12ab"},
					"end":     map[string]any{"type": "string", "description": "Last line's anchor if more than one line"},
					"oldText": map[string]any{"type": "string", "description": "Exact unique text, when not using anchors"},
					"newText": map[string]any{"type": "string"},
				},
				"required": []string{"newText"},
			},
		}
		params["properties"] = properties
		schema.Parameters = params
	}
	return schema
}
