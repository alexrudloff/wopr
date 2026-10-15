package codingagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

// TestAppendTerminatesAnUnterminatedLastLine pins the append path against a
// session file whose last record lost its newline, which is what an interrupted
// write leaves behind. The append has to terminate that record before writing
// its own: glued together, the two form one line that parses as neither, so the
// record that was already on disk is destroyed and the new one is lost with it.
//
// Reverting appendSessionLine to an O_WRONLY write with no last-byte read loses
// the earlier record and this fails, which is the only thing that would notice.
func TestAppendTerminatesAnUnterminatedLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unterminated.jsonl")

	header := `{"type":"session","version":3,"id":"unterminated","timestamp":"2024-01-01T00:00:00.000000000Z","cwd":"/test"}` + "\n"
	// No trailing newline on the last record: the shape an interrupted write leaves.
	existing := `{"type":"message","id":"first-entry","parentId":null,"timestamp":"2024-01-01T00:00:01.000000000Z","message":{"role":"assistant","content":[{"type":"text","text":"first"}],"api":"faux","provider":"test-faux","model":"faux-1","stopReason":"stop","timestamp":1704067201000}}`
	if err := os.WriteFile(path, []byte(header+existing), 0o644); err != nil {
		t.Fatalf("seed the session file: %v", err)
	}

	sm := NewSessionManagerWithDir(dir, dir)
	sess, err := sm.Load(path)
	if err != nil {
		t.Fatalf("load the unterminated session: %v", err)
	}
	if _, err := sess.AppendMessage(mkAssistantMsg("second")); err != nil {
		t.Fatalf("append onto the unterminated file: %v", err)
	}

	// The records as they read back, not the bytes: the bytes of a correctly
	// appended file and a glued one differ only in the newline.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = f.Close() }()
	var got []string
	if err := forEachJSONLLine(f, func(line []byte) error {
		if len(line) == 0 {
			return nil
		}
		var entry MessageEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("record %q did not parse, so the record before it was destroyed: %w", line, err)
		}
		if entry.Message.Assistant == nil {
			return nil // the header
		}
		for _, block := range entry.Message.Assistant.Content {
			if text, ok := block.(ai.TextContent); ok {
				got = append(got, text.Text)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{"first", "second"}
	if !slices.Equal(got, want) {
		t.Fatalf("assistant records: expected %v in order, got %v", want, got)
	}
}
