package tools

import (
	"fmt"
	"strings"
)

// Loose matching: when an oldText isn't found as written (even after the
// fuzzy normalization), these whole-line matchers try progressively looser
// comparisons. Each must find exactly one place; a loose match that fits
// two places fails as ambiguous rather than guess. A match covers whole
// lines and the same number of lines as oldText.

// looseMatcher compares one line of oldText with one line of the file and,
// for a whole block, may adjust newText the same way the file differs.
type looseMatcher struct {
	label string
	// same reports whether an old line matches a file line.
	same func(oldLine, fileLine string) bool
	// adapt rewrites newText for the matched block, or nil to keep it.
	adapt func(oldLines, fileLines []string, newText string) (string, bool)
}

var looseMatchers = []looseMatcher{
	{label: "ignoring indentation", same: sameIgnoringIndent, adapt: reindent},
	{label: "ignoring whitespace", same: func(a, b string) bool { return collapseSpace(a) == collapseSpace(b) }},
	{label: "ignoring doubled backslashes", same: func(a, b string) bool { return collapseBackslashes(a) == collapseBackslashes(b) }, adapt: undoubleBackslashes},
}

// looseMatch is a loose matcher's hit: a byte range of the content and
// the newText to put there.
type looseMatch struct {
	index, length int
	newText       string
	label         string
}

// findLoose tries each loose matcher in order. ambiguous is the number of
// places the first matcher that matched more than once found.
func findLoose(content, oldText, newText string) (m looseMatch, ok bool, ambiguous int, label string) {
	trailingNewline := strings.HasSuffix(oldText, "\n")
	oldLines := strings.Split(strings.TrimSuffix(oldText, "\n"), "\n")
	if strings.TrimSpace(oldText) == "" {
		return looseMatch{}, false, 0, ""
	}
	spans := getLineSpans(content)
	lines := make([]string, len(spans))
	for i, s := range spans {
		lines[i] = strings.TrimSuffix(content[s.start:s.end], "\n")
	}
	for _, matcher := range looseMatchers {
		var hits []int
		for start := 0; start+len(oldLines) <= len(lines); start++ {
			if blockMatches(matcher, oldLines, lines[start:start+len(oldLines)]) {
				hits = append(hits, start)
			}
		}
		switch {
		case len(hits) > 1:
			return looseMatch{}, false, len(hits), matcher.label
		case len(hits) == 0:
			continue
		}
		start, end := hits[0], hits[0]+len(oldLines)
		fileLines := lines[start:end]
		replacement := newText
		if matcher.adapt != nil {
			adapted, ok := matcher.adapt(oldLines, fileLines, newText)
			if !ok {
				continue
			}
			replacement = adapted
		}
		from := spans[start].start
		to := spans[end-1].start + len(fileLines[len(fileLines)-1])
		if trailingNewline && to < len(content) {
			to++ // the line's own newline, which oldText ended with
		}
		return looseMatch{index: from, length: to - from, newText: replacement, label: matcher.label}, true, 0, ""
	}
	return looseMatch{}, false, 0, ""
}

func blockMatches(matcher looseMatcher, oldLines, fileLines []string) bool {
	for i := range oldLines {
		if !matcher.same(oldLines[i], fileLines[i]) {
			return false
		}
	}
	return true
}

func leadingSpace(line string) string { return line[:len(line)-len(strings.TrimLeft(line, " \t"))] }

func sameIgnoringIndent(oldLine, fileLine string) bool {
	return strings.TrimLeft(oldLine, " \t") == strings.TrimLeft(fileLine, " \t")
}

// reindent shifts newText's indentation by the shift between oldText and
// the file, when the shift is the same on every non-blank line; otherwise
// the match isn't safe to apply.
func reindent(oldLines, fileLines []string, newText string) (string, bool) {
	var add, remove string
	found := false
	for i, old := range oldLines {
		if strings.TrimSpace(old) == "" {
			continue
		}
		o, f := leadingSpace(old), leadingSpace(fileLines[i])
		var a, r string
		switch {
		case strings.HasPrefix(f, o):
			a = f[len(o):]
		case strings.HasPrefix(o, f):
			r = o[len(f):]
		default:
			return "", false
		}
		if found && (a != add || r != remove) {
			return "", false
		}
		add, remove, found = a, r, true
	}
	if !found {
		return newText, true
	}
	lines := strings.Split(newText, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if remove != "" {
			if !strings.HasPrefix(line, remove) {
				return "", false
			}
			line = line[len(remove):]
		}
		lines[i] = add + line
	}
	return strings.Join(lines, "\n"), true
}

// collapseSpace trims a line and collapses runs of spaces and tabs.
func collapseSpace(line string) string { return strings.Join(strings.Fields(line), " ") }

// collapseBackslashes collapses runs of backslashes to one.
func collapseBackslashes(line string) string {
	var b strings.Builder
	prev := false
	for _, r := range line {
		if r == '\\' && prev {
			continue
		}
		prev = r == '\\'
		b.WriteRune(r)
	}
	return b.String()
}

// undoubleBackslashes applies the matched mistake's fix to newText: an
// oldText that doubled the file's backslashes (\\u00b7 for ·) came
// from over-escaping the call's JSON, so newText was over-escaped the same
// way.
func undoubleBackslashes(oldLines, fileLines []string, newText string) (string, bool) {
	old, file := strings.Join(oldLines, "\n"), strings.Join(fileLines, "\n")
	if strings.Count(old, `\\`) <= strings.Count(file, `\\`) {
		return "", false // not the doubling mistake: don't guess
	}
	return strings.ReplaceAll(newText, `\\`, `\`), true
}

// nearestHint names the lines of content most like oldText, for an edit
// nothing matched, so the model can fix that one oldText without reading
// the file again.
func nearestHint(content, oldText string) string {
	oldLines := strings.Split(strings.TrimSuffix(oldText, "\n"), "\n")
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(oldLines) == 0 || len(oldLines) > len(lines) {
		return ""
	}
	// Each line counts by its length, so a long line that nearly matches
	// outweighs a short one that doesn't.
	weight := 0
	for _, old := range oldLines {
		weight += max(1, len(strings.TrimSpace(old)))
	}
	best, bestScore := -1, 0.0
	for start := 0; start+len(oldLines) <= len(lines); start++ {
		score := 0.0
		for i, old := range oldLines {
			score += similarity(old, lines[start+i]) * float64(max(1, len(strings.TrimSpace(old))))
		}
		if score /= float64(weight); score > bestScore {
			best, bestScore = start, score
		}
	}
	if best < 0 || bestScore < 0.5 {
		return ""
	}
	shown := lines[best : best+len(oldLines)]
	more := ""
	if len(shown) > 12 {
		more = fmt.Sprintf("\n… (%d more lines)", len(shown)-12)
		shown = shown[:12]
	}
	hint := fmt.Sprintf("\nClosest text is lines %d-%d (%d%% similar):\n%s%s", best+1, best+len(oldLines), int(bestScore*100), strings.Join(shown, "\n"), more)
	region := strings.Join(lines[best:best+len(oldLines)], "\n")
	if strings.Contains(region, `\`) && (!strings.Contains(oldText, `\`) || strings.Count(oldText, `\\`) > strings.Count(region, `\\`)) {
		hint += "\nThese lines contain backslash sequences written out in the file (such as \\u00b7). In your call's JSON, write each of those backslashes as \\\\, or start or end the oldText away from them."
	}
	return hint
}

// similarity is the share of the longer line's characters the two lines
// have in common, by bigrams, from 0 to 1.
func similarity(a, b string) float64 {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return 1
	}
	// A line cut short (or one that runs on) is nearly the same line.
	if short, long := min(len(a), len(b)), max(len(a), len(b)); short >= 8 && short < long && (strings.Contains(b, a) || strings.Contains(a, b)) {
		return 0.9
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	grams := map[string]int{}
	for i := 0; i+1 < len(a); i++ {
		grams[a[i:i+2]]++
	}
	common := 0
	for i := 0; i+1 < len(b); i++ {
		if grams[b[i:i+2]] > 0 {
			grams[b[i:i+2]]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(a)+len(b)-2)
}
