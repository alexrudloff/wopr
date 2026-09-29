package tempfiles

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/internal/testenv"
)

// Deletion only ever touches tracked, owned entries inside a temp root; it
// never follows a symlink, leaves a live owner's entries alone, sweeps a
// dead owner's, and on compaction skips recently used or still-mentioned
// files.
func TestCleanupDeletesOnlyWhatIsSafe(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	tr := &Tracker{registry: filepath.Join(t.TempDir(), RegistryFileName), roots: []string{root}, pid: os.Getpid(), snapshots: map[string]map[string]map[string]bool{}}
	tr.start, _ = processStart(tr.pid)

	old := time.Now().Add(-time.Hour)
	file := func(dir, name string, age time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, age, age); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	untracked := file(root, "untracked", old)
	outsideFile := file(outside, "outside", old)
	target := file(outside, "target", old)
	link := filepath.Join(root, "link")
	testenv.Symlink(t, target, link)
	stale := file(root, "stale.png", old)
	recent := file(root, "recent.png", time.Now())
	mentioned := file(root, "probe.mjs", old)
	tr.record("s1", stale, recent, mentioned, link)
	_ = tr.update(func(e []Entry) []Entry {
		return append(e, Entry{Path: outsideFile, SessionID: "s1", PID: tr.pid, ProcessStart: tr.start})
	})

	// Compaction: only the stale, unmentioned file goes.
	tr.CleanUnreferenced("s1", "the summary still uses probe.mjs")
	if exists(stale) || !exists(recent) || !exists(mentioned) {
		t.Fatalf("compaction: stale=%v recent=%v mentioned=%v, want false true true", exists(stale), exists(recent), exists(mentioned))
	}

	// Leaving the session: the link itself goes, never its target; nothing
	// untracked or outside a root is touched.
	tr.CleanSession("s1", false)
	if exists(link) || !exists(target) || !exists(untracked) || !exists(outsideFile) {
		t.Fatalf("session: link=%v target=%v untracked=%v outside=%v", exists(link), exists(target), exists(untracked), exists(outsideFile))
	}

	// Sweep: a live owner's entry stays, a dead owner's goes.
	live := file(root, "live", old)
	dead := file(root, "dead", old)
	_ = tr.update(func(e []Entry) []Entry {
		return append(e,
			Entry{Path: live, SessionID: "s2", PID: tr.pid, ProcessStart: tr.start},
			Entry{Path: dead, SessionID: "s3", PID: tr.pid, ProcessStart: "not-this-process"})
	})
	tr.SweepDead()
	if !exists(live) || exists(dead) {
		t.Fatalf("sweep: live=%v dead=%v, want true false", exists(live), exists(dead))
	}
}
