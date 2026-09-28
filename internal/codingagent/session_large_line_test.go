package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLargeLineSession writes a session whose message entry is one JSONL
// line longer than 16 MiB, as an entry with a large image produces.
func writeLargeLineSession(t *testing.T, textBytes int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "large.jsonl")
	header := `{"type":"session","version":3,"id":"s1","timestamp":"2026-01-01T00:00:00Z","cwd":"/project"}`
	entry, err := json.Marshal(map[string]any{
		"type": "message", "id": "e1", "parentId": nil, "timestamp": "2026-01-01T00:00:01Z",
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", textBytes)}}, "timestamp": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(header+"\n"+string(entry)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Session files are read whole with no line cap, so an entry larger
// than 16 MiB still loads and lists.
func TestSessionFileWithLineOver16MiBLoads(t *testing.T) {
	const textBytes = 17 << 20
	path := writeLargeLineSession(t, textBytes)
	sess, err := NewSessionManagerWithDir(t.TempDir(), filepath.Dir(path)).Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := sess.Entries()
	if len(entries) != 1 || len(entries[0].Raw()) < textBytes {
		t.Fatalf("entries = %d, want the one large entry intact", len(entries))
	}
	info, err := summarizeSessionFile(path)
	if err != nil {
		t.Fatalf("summarizeSessionFile: %v", err)
	}
	if info.MessageCount != 1 || info.ID != "s1" {
		t.Fatalf("summary = %+v, want one message in session s1", info)
	}
}
