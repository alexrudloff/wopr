package codingagent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FindMostRecentForContinue must not resume another project's session when
// the session dir is a custom (non-cwd-encoded) directory shared across
// projects: it filters to the newest session whose header cwd matches the
// runtime cwd, even when a different project's session is newer.
func TestFindMostRecentForContinue_FiltersByCwd(t *testing.T) {
	dir := t.TempDir() // custom shared session dir, not the cwd-encoded default

	write := func(name, headerCwd, text string, mtime time.Time) string {
		path := filepath.Join(dir, name)
		header := fmt.Sprintf(`{"type":"session","version":%d,"id":%q,"timestamp":"2025-01-01T00:00:00.000Z","cwd":%q}`,
			CurrentSessionVersion, name, headerCwd)
		msg := fmt.Sprintf(`{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, text)
		if err := os.WriteFile(path, []byte(header+"\n"+msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return path
	}

	now := time.Now()
	projA := "/projects/alpha"
	projB := "/projects/beta"
	wantA := write("a.jsonl", projA, "alpha", now.Add(-time.Minute))
	write("b.jsonl", projB, "beta", now) // newer, but a different project

	smA := NewSessionManagerWithDir(projA, dir)
	if got := smA.FindMostRecentForContinue(); got != wantA {
		t.Errorf("FindMostRecentForContinue=%q want %q (cwd-matched alpha, not newer beta)", got, wantA)
	}

	// No session matches the cwd: resume nothing rather than another project's.
	smGamma := NewSessionManagerWithDir("/projects/gamma", dir)
	if got := smGamma.FindMostRecentForContinue(); got != "" {
		t.Errorf("FindMostRecentForContinue=%q want empty for unmatched cwd", got)
	}

	// Plain FindMostRecent stays cwd-agnostic (newest wins) so the
	// picker and other callers are unaffected by the continue filter.
	if got := smGamma.FindMostRecent(); got == "" {
		t.Errorf("FindMostRecent should still return the newest session regardless of cwd")
	}
}

// TestFindMostRecentTieBreaksByCreated covers a CI flake: on a coarse-mtime
// filesystem two sessions written back-to-back share an identical mtime, so a
// pure-mtime sort leaves their order undefined and FindMostRecent can return
// the older one. The header creation timestamp (RFC3339Nano) breaks the tie
// deterministically. Filenames are chosen so the lexically-first file is the
// OLDER session, so the unfixed mtime-only sort returns it (red) and the
// Created tie-break returns the newer one (green).
func TestFindMostRecentTieBreaksByCreated(t *testing.T) {
	dir := t.TempDir()
	sameMtime := time.Now().Truncate(time.Second)

	write := func(name, created string) string {
		path := filepath.Join(dir, name)
		header := fmt.Sprintf(`{"type":"session","version":%d,"id":%q,"timestamp":%q,"cwd":"/p"}`,
			CurrentSessionVersion, name, created)
		msg := `{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`
		if err := os.WriteFile(path, []byte(header+"\n"+msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, sameMtime, sameMtime); err != nil {
			t.Fatal(err)
		}
		return path
	}

	write("a.jsonl", "2025-01-01T00:00:00.000000001Z")              // lexically first, older
	wantNewer := write("b.jsonl", "2025-01-01T00:00:00.000000002Z") // lexically second, newer

	sm := NewSessionManagerWithDir("/p", dir)
	if got := sm.FindMostRecent(); got != wantNewer {
		t.Errorf("FindMostRecent=%q want %q (later-created wins the equal-mtime tie)", got, wantNewer)
	}
}
