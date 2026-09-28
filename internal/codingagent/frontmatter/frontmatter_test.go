package frontmatter

import (
	"strings"
	"testing"
)

func TestParseAgentFrontmatter(t *testing.T) {
	src := `---
name: worker
description: Implements tasks from todos
tools: read,bash,write,edit
model: claude-sonnet-4.6
auto-exit: true
---

# Worker Agent

Body text here.
`
	d := Parse(src)
	if d.String("name") != "worker" {
		t.Errorf("name: %q", d.String("name"))
	}
	if d.String("description") != "Implements tasks from todos" {
		t.Errorf("description: %q", d.String("description"))
	}
	if !d.Bool("auto-exit") {
		t.Errorf("auto-exit should be true")
	}
	if !strings.Contains(d.Body, "# Worker Agent") {
		t.Errorf("body should contain heading, got %q", d.Body)
	}
}

func TestParseReportsMalformedYAML(t *testing.T) {
	d := Parse("---\ndescription: [unterminated\n---\nbody")
	if d.Err == nil {
		t.Fatal("malformed YAML was accepted")
	}
}
