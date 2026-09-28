package codingagent

// crash_log.go: crash persistence for
// JavaScript-style and Go panic stack frames.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	maxCrashRecords = 5
	maxCrashAge     = 7 * 24 * time.Hour
)

// CrashRecord is one crashes.json entry. Kind is "uncaught_exception" or
// "fatal_error".
type CrashRecord struct {
	Timestamp   string  `json:"timestamp"`
	Version     string  `json:"version"`
	Kind        string  `json:"kind"`
	Message     string  `json:"message"`
	Stack       *string `json:"stack"`
	SessionFile *string `json:"sessionFile"`
	CWD         string  `json:"cwd"`
	Notified    bool    `json:"notified,omitempty"`
}

// CrashLogPath returns the crash log location for an agent directory.
func CrashLogPath(agentDir string) string {
	return filepath.Join(agentDir, "crashes.json")
}

// ReadCrashLog returns the valid records, or none when the file is missing
// or unreadable. A record needs a string timestamp and message.
func ReadCrashLog(path string) []CrashRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	records := make([]CrashRecord, 0, len(raw))
	for _, item := range raw {
		var fields map[string]any
		if json.Unmarshal(item, &fields) != nil {
			continue
		}
		if _, ok := fields["timestamp"].(string); !ok {
			continue
		}
		if _, ok := fields["message"].(string); !ok {
			continue
		}
		var record CrashRecord
		if json.Unmarshal(item, &record) != nil {
			continue
		}
		records = append(records, record)
	}
	return records
}

func writeCrashLog(records []CrashRecord, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := marshalJSONIndent(records)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// CrashInput describes a crash to record.
type CrashInput struct {
	Kind        string
	Message     string
	Stack       string
	SessionFile string
	CWD         string
	Version     string
}

// RecordCrash appends a crash, keeping the newest five. It is best effort for
// callers that are already crashing: it reports ok=false when nothing was
// written.
func RecordCrash(crash CrashInput, path string, now time.Time) (CrashRecord, bool) {
	record := CrashRecord{
		Timestamp: isoTimestamp(now),
		Version:   crash.Version,
		Kind:      crash.Kind,
		Message:   crash.Message,
		CWD:       crash.CWD,
	}
	if crash.Stack != "" {
		record.Stack = &crash.Stack
	}
	if crash.SessionFile != "" {
		record.SessionFile = &crash.SessionFile
	}
	records := append(ReadCrashLog(path), record)
	if len(records) > maxCrashRecords {
		records = records[len(records)-maxCrashRecords:]
	}
	if writeCrashLog(records, path) != nil {
		return CrashRecord{}, false
	}
	return record, true
}

// TakeUnnotifiedCrash returns the newest crash from the last seven days that
// has not been announced, and marks every record announced.
func TakeUnnotifiedCrash(path string, now time.Time) (CrashRecord, bool) {
	records := ReadCrashLog(path)
	for _, record := range slices.Backward(records) {
		if record.Notified {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil || now.Sub(at) > maxCrashAge {
			continue
		}
		for j := range records {
			records[j].Notified = true
		}
		// Showing the notice again is harmless, so a write failure is ignored.
		_ = writeCrashLog(records, path)
		return record, true
	}
	return CrashRecord{}, false
}

// ClearCrashLog removes the crash log; a failure leaves the records for the
// next report.
func ClearCrashLog(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// crashNotice is the startup warning for an unannounced crash, with the time
// in en-US locale form.
func crashNotice(crash CrashRecord) string {
	when := crash.Timestamp
	if at, err := time.Parse(time.RFC3339Nano, crash.Timestamp); err == nil {
		when = at.Local().Format("1/2/2006, 3:04:05 PM")
	}
	return fmt.Sprintf("%s crashed on %s (%s). Run /bug to report it; the crash details are attached automatically.", AppName, when, crash.Message)
}

// crashReportInstructions tells the user how to report a crash.
func crashReportInstructions(sessionFile string) string {
	resume := "start " + AppName + " and"
	if sessionFile != "" {
		resume = "run `" + AppName + " -r` to resume the session, then"
	}
	return "To report this crash: " + resume + " run /bug. The crash details are attached automatically."
}
