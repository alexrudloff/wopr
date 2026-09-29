package coding

import (
	"os"
	"path/filepath"
	"testing"
)

// Undo must put back exact bytes: a file the model replaced, a file it
// created, two undos in a row, and a file changed after the model's change,
// whose newer version stays recoverable.
func TestUndoRestoresExactBytes(t *testing.T) {
	work, dir := t.TempDir(), t.TempDir()
	game := filepath.Join(work, "index.html")
	created := filepath.Join(work, "new.js")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	change := func(call, path, text string) {
		t.Helper()
		before := readFileState(path)
		write(path, text)
		entry := undoEntry{Kind: "change", Tool: "write", Call: call, Path: path, Absent: !before.exists, Mode: uint32(before.mode), After: readFileState(path).sum()}
		if err := appendUndoEntry(dir, &entry, before); err != nil {
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

	write(game, "batman\x00\n")
	change("c1", game, "engine v1\n")
	change("c2", created, "helper\n")
	change("c3", game, "engine v2\n")
	write(game, "user tweak\n") // after the tool's change

	got, err := undoIn(dir, work, "")
	if err != nil || len(got) != 1 || !got[0].ChangedSince || got[0].Saved == "" {
		t.Fatalf("first undo = %+v, %v", got, err)
	}
	if read(game) != "engine v1\n" || read(got[0].Saved) != "user tweak\n" {
		t.Fatalf("after first undo: file %q, saved %q", read(game), read(got[0].Saved))
	}
	if _, err := undoIn(dir, work, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("undoing a created file left it behind: %v", err)
	}
	if _, err := undoIn(dir, work, "index.html"); err != nil {
		t.Fatal(err)
	}
	if read(game) != "batman\x00\n" {
		t.Fatalf("original not restored byte for byte: %q", read(game))
	}
	if _, err := undoIn(dir, work, ""); err == nil {
		t.Fatal("undo with nothing left succeeded")
	}
}
