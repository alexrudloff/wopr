package coding

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Shell edits must be undoable to exact bytes: a sed -i on a file the model
// read, a rewrite of a clean tracked file it never read (restored from the
// index), and a heredoc-created file (removed). A file already dirty before
// the command, and outside git a file nothing named, are left alone.
func TestShellEditsAreUndoable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repo, store := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	path := func(name string) string { return canonical(filepath.Join(repo, name)) }
	watched, tracked, dirty, created := path("a.txt"), path("b.txt"), path("dirty.txt"), path("new.txt")

	git("init", "-q")
	write(watched, "alpha\x00\n")
	write(tracked, "bravo\n")
	write(dirty, "clean\n")
	git("add", ".")
	git("commit", "-qm", "base")
	write(dirty, "dirty before\n")

	record := func(snap pendingSnapshot, callID string, after fileState) error {
		entry := undoEntry{Kind: "change", Tool: snap.tool, Call: callID, Path: snap.path, Absent: !snap.before.exists,
			Mode: uint32(snap.before.mode), After: after.sum()}
		return appendUndoEntry(store, &entry, snap.before)
	}
	c := shellCapture{dir: store, cwd: repo, edits: &shellEdits{}, record: record}
	run := func(call, command string, change func()) int {
		t.Helper()
		c.before(ctx, call, command)
		change()
		return len(c.after(ctx, call, command))
	}

	c.copyFile(watched) // the model read it
	if n := run("sed", "sed -i '' s/alpha/ALPHA/ a.txt", func() { write(watched, "ALPHA\x00\n") }); n != 1 {
		t.Fatalf("sed -i on a watched file recorded %d changes, want 1", n)
	}
	if n := run("py", "python3 - <<'EOF'\n...\nEOF", func() { write(tracked, "rewritten\n") }); n != 1 {
		t.Fatalf("rewrite of a clean tracked file recorded %d changes, want 1", n)
	}
	if n := run("cat", "cat > new.txt <<'EOF'\nhi\nEOF", func() { write(created, "hi\n") }); n != 1 {
		t.Fatalf("heredoc-created file recorded %d changes, want 1", n)
	}
	if n := run("dirty", "echo more >> dirty.txt", func() { write(dirty, "dirty before\nmore\n") }); n != 0 {
		t.Fatalf("a file dirty before the command was recorded (%d changes)", n)
	}

	outside := t.TempDir()
	o := shellCapture{dir: t.TempDir(), cwd: outside, edits: &shellEdits{}, record: record}
	o.before(ctx, "stray", "true")
	write(filepath.Join(outside, "stray.txt"), "x")
	if got := o.after(ctx, "stray", "true"); len(got) != 0 {
		t.Fatalf("outside git, an unnamed new file was recorded: %+v", got)
	}

	// Outside git, a sed -i on a file the model only grepped: the command
	// names it, so it was copied before and the edit is undoable.
	outsideStore := t.TempDir()
	config := canonical(filepath.Join(outside, "tsconfig.json"))
	write(config, `{"include": ["src"]}`+"\n")
	o = shellCapture{dir: outsideStore, cwd: outside, edits: &shellEdits{}, record: func(snap pendingSnapshot, callID string, after fileState) error {
		entry := undoEntry{Kind: "change", Tool: snap.tool, Call: callID, Path: snap.path, Absent: !snap.before.exists,
			Mode: uint32(snap.before.mode), After: after.sum()}
		return appendUndoEntry(outsideStore, &entry, snap.before)
	}}
	sed := "sed -i '' 's/src/src,types/' tsconfig.json"
	o.before(ctx, "sed", sed)
	write(config, `{"include": ["src","types"]}`+"\n")
	if got := o.after(ctx, "sed", sed); len(got) != 1 {
		t.Fatalf("outside git, a named file's edit was not recorded: %+v", got)
	}
	if _, err := undoIn(outsideStore, outside, ""); err != nil {
		t.Fatal(err)
	}
	if got := read(config); got != `{"include": ["src"]}`+"\n" {
		t.Fatalf("outside git, undo restored %q", got)
	}

	undo := func() {
		t.Helper()
		if _, err := undoIn(store, repo, ""); err != nil {
			t.Fatal(err)
		}
	}
	undo()
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("undo left the created file: %v", err)
	}
	undo()
	if got := read(tracked); got != "bravo\n" {
		t.Fatalf("tracked file restored to %q, want the index copy", got)
	}
	undo()
	if got := read(watched); got != "alpha\x00\n" {
		t.Fatalf("watched file restored to %q, want its exact bytes", got)
	}
	if got := read(dirty); got != "dirty before\nmore\n" {
		t.Fatalf("an unrecorded file was touched: %q", got)
	}
}
