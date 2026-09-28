package main

import (
	"slices"
	"testing"
)

func TestParseFlags(t *testing.T) {
	f := parseFlags([]string{"--model", "example/model", "--provider", "example", "--no-session", "-t", "read,bash", "-na", "@prompt.md", "hello"})
	if f.Model != "example/model" || f.Provider != "example" || !f.NoSession {
		t.Fatalf("flags = %+v", f)
	}
	if !slices.Equal(f.Tools, []string{"read", "bash"}) || !slices.Equal(f.FileArgs, []string{"prompt.md"}) || !slices.Equal(f.Args, []string{"hello"}) {
		t.Fatalf("tools=%v files=%v args=%v", f.Tools, f.FileArgs, f.Args)
	}
	if f.ProjectTrustOverride == nil || *f.ProjectTrustOverride {
		t.Fatalf("-na trust override = %v, want false", f.ProjectTrustOverride)
	}

	// -p/--print is a bare boolean: the prompt stays positional.
	p := parseFlags([]string{"-p", "hello"})
	if p.Print == "" || !slices.Equal(p.Args, []string{"hello"}) {
		t.Fatalf("print = %q args = %v", p.Print, p.Args)
	}
	// --resume never consumes the next flag.
	r := parseFlags([]string{"--resume", "--model", "gpt-4o"})
	if !r.ResumeAny || r.Model != "gpt-4o" {
		t.Fatalf("resume flags = %+v", r)
	}
	if bad := parseFlags([]string{"--thinking", "bogus"}); bad.Thinking != "" {
		t.Fatalf("invalid thinking level accepted: %q", bad.Thinking)
	}
}
