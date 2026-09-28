package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func crashInput(message string) CrashInput {
	return CrashInput{Kind: "uncaught_exception", Message: message, Stack: "stack of " + message, SessionFile: "/s/session.jsonl", CWD: "/work", Version: "1.2.3"}
}

// RecordCrash writes pretty JSON with a trailing newline, null for
// a missing stack or session file, and only the newest five records kept.
func TestRecordCrashKeepsTheNewestFive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "crashes.json")
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	for i := range 6 {
		if _, ok := RecordCrash(crashInput(string(rune('a'+i))), path, now.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("record %d failed", i)
		}
	}
	records := ReadCrashLog(path)
	if len(records) != 5 || records[0].Message != "b" || records[4].Message != "f" {
		t.Fatalf("records = %+v", records)
	}
	first := records[0]
	if first.Timestamp != "2026-09-23T08:00:01.000Z" || first.Version != "1.2.3" || first.Kind != "uncaught_exception" || first.CWD != "/work" || first.Stack == nil || *first.Stack != "stack of b" || first.SessionFile == nil {
		t.Fatalf("record = %+v", first)
	}
	record, ok := RecordCrash(CrashInput{Kind: "fatal_error", Message: "bare", CWD: "/w"}, path, now)
	if !ok || record.Stack != nil || record.SessionFile != nil {
		t.Fatalf("bare record = %+v %v", record, ok)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "[\n  {\n    \"timestamp\": ") || !strings.HasSuffix(string(data), "]\n") || !strings.Contains(string(data), `"stack": null,`) || !strings.Contains(string(data), `"sessionFile": null,`) {
		t.Fatalf("crash log format:\n%s", data)
	}
}
