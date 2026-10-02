package coding

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A file one running writer changed is refused to another writer and to the
// orchestrator, naming the holder, until the holder finishes.
func TestWriterFileLock(t *testing.T) {
	cwd := t.TempDir()
	var locks fileLocks
	edit := json.RawMessage(`{"path":"game.js","edits":[{"oldText":"a","newText":"b"}]}`)
	paths := lockedPaths(cwd, "edit", edit)
	if len(paths) != 1 || paths[0] != filepath.Join(cwd, "game.js") {
		t.Fatalf("paths = %v", paths)
	}

	if path, holder := locks.claim("task_a", "add sound", paths); holder != "" {
		t.Fatalf("first claim refused: %s held by %s", path, holder)
	}
	if _, holder := locks.claim("task_a", "add sound", paths); holder != "" {
		t.Fatal("a writer was refused its own file")
	}
	path, holder := locks.claim("task_b", "fix scoring", paths)
	if holder != "task_a" {
		t.Fatalf("second writer got the file: holder %q", holder)
	}
	if reason := locks.lockRefusal(cwd, path, holder); !strings.Contains(reason, "game.js") || !strings.Contains(reason, "task_a (add sound)") {
		t.Fatalf("refusal = %q", reason)
	}
	if _, holder := locks.held(paths); holder != "task_a" {
		t.Fatalf("orchestrator not refused: holder %q", holder)
	}

	locks.release("task_a")
	if _, holder := locks.claim("task_b", "fix scoring", paths); holder != "" {
		t.Fatalf("file still held after its writer finished: %s", holder)
	}
}
