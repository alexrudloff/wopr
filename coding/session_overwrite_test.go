package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A write that would replace an unrelated file the session found already
// there is refused; overwrite: true and a file the session changed pass.
func TestOverwriteGuard(t *testing.T) {
	dir := t.TempDir()
	game := "<!DOCTYPE html>\n<body>\n<title>LEGO Batman</title>\nconst hero = 1\nfunction jump() {}\nfunction fly() {}\n</body>\n"
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte(game), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute)
	args := func(content string, overwrite bool) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"path": "index.html", "content": content, "overwrite": overwrite})
		return raw
	}
	// Shared boilerplate (doctype, body tags) doesn't make it the same file.
	engine := "<!DOCTYPE html>\n<body>\n<title>Rocket engine</title>\nconst rde = 1\nfunction spin() {}\n</body>\n"

	if reason := overwriteCheck(dir, args(engine, false), started); !strings.Contains(reason, "LEGO Batman") {
		t.Fatalf("an unrelated replacement wasn't refused: %q", reason)
	}
	if reason := overwriteCheck(dir, args(game+"function glide() {}\n", false), started); reason != "" {
		t.Fatalf("a rewrite of the same file was refused: %q", reason)
	}
	if reason := overwriteCheck(dir, args(engine, true), started); reason != "" {
		t.Fatalf("overwrite: true was refused: %q", reason)
	}
	// The session changed it, so it's the session's to rewrite.
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if reason := overwriteCheck(dir, args(engine, false), started); reason != "" {
		t.Fatalf("a file changed this session was refused: %q", reason)
	}
}
