package tools

import (
	"strings"
	"testing"
)

// Loose matching may only edit the one place it is sure of, and must
// adjust newText the way the file differs from oldText.
func TestLooseEditMatching(t *testing.T) {
	apply := func(content, oldText, newText string) (string, error) {
		t.Helper()
		applied, err := applyEditsToNormalizedContent(content, []editEntry{{OldText: oldText, NewText: newText}}, "game.js")
		return applied.newContent, err
	}

	// Exact wins: the text as written is used, not re-indented.
	got, err := apply("  run()\nstop()\n", "run()\nstop()", "walk()\nstop()")
	if err != nil || got != "  walk()\nstop()\n" {
		t.Fatalf("exact: %q, %v", got, err)
	}

	// Indentation shifted: newText is re-indented by the same shift.
	got, err = apply("func a() {\n\t\tif x {\n\t\t\treturn 1\n\t\t}\n}\n", "if x {\n\treturn 1\n}", "if x {\n\treturn 2\n}")
	if err != nil || got != "func a() {\n\t\tif x {\n\t\t\treturn 2\n\t\t}\n}\n" {
		t.Fatalf("reindent: %q, %v", got, err)
	}

	// Two places fit loosely: nothing changes.
	twice := "  if x {\n    go()\n  }\n    if x {\n      go()\n    }\n"
	if got, err = apply(twice, "if x {\n  go()\n}", "if y {\n  go()\n}"); err == nil || !strings.Contains(err.Error(), "matched 2 places") {
		t.Fatalf("ambiguous: %q, %v", got, err)
	}

	// From a real session: the file has the literal escape · and the
	// model doubled its backslash in both oldText and newText.
	file := "function gameOver() {\n  $('ovText').innerHTML = `SCORE ${score} \\u00b7 ${kos} KOs`;\n}\n"
	got, err = apply(file, "  $('ovText').innerHTML = `SCORE ${score} \\\\u00b7 ${kos} KOs`;", "  $('ovText').innerHTML = `SCORE ${score} \\\\u00b7 ${kos} KOs \\\\u00b7 best`;")
	if err != nil || !strings.Contains(got, "${kos} KOs \\u00b7 best`;") || strings.Contains(got, `\\u00b7`) {
		t.Fatalf("doubled backslashes: %q, %v", got, err)
	}

	// Nothing matches: the error points at the closest lines and the escape.
	_, err = apply("  <div id=\"help\">WASD move \\u00b7 SHIFT run</div>\n  <p>x</p>\n", "  <div id=\"help\">WASD move \ninline_unused", "x")
	if err == nil || !strings.Contains(err.Error(), "Closest text is lines 1-2") || !strings.Contains(err.Error(), "backslash") {
		t.Fatalf("hint: %v", err)
	}
}
