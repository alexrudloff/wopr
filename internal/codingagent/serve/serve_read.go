package serve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	codinghost "github.com/alexrudloff/wopr/coding/host"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/sessionblob"
)

// ─── walkBack: the one backward primitive ────────────────────────────────

// walkStop controls when walkBack stops reading.
// It receives the count of complete records seen so far.
type walkStop func(completeRecords int) bool

// walkBack opens path, seeks to end, and walks backwards in tailBlockBytes
// blocks until stop is satisfied or byteLimit is reached. Returns records
// oldest-first (after dropping the first partial record).
// stop is called with the count of complete records seen so far.
// The byte limit is an independent bound: reading stops on whichever bound is reached first.
// Note: the byte limit may be reached before the record limit, in which case
// fewer records than requested are returned. This is expected behavior for
// very large entries that exceed the byte limit before the record count is met.
// Callers should be aware that tailEntries may return fewer than 'limit' records
// when entries are very large (exceeding maxTailBytes/limit per entry).
func walkBack(path string, byteLimit int64, stop walkStop) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size == 0 {
		return nil, nil
	}

	var (
		buf         = make([]byte, tailBlockBytes)
		records     [][]byte
		readBytes   int64
		offset      = size
		carried     []byte // partial line carried from previous (older) block
		recordCount int    // number of complete records seen so far
	)

	for offset > 0 && readBytes < byteLimit {
		readSize := tailBlockBytes
		if offset < int64(readSize) {
			readSize = int(offset)
		}
		offset -= int64(readSize)

		if _, err := f.ReadAt(buf[:readSize], offset); err != nil && err != io.EOF {
			return nil, err
		}

		// Clone the block to avoid aliasing the same buffer (BLOCKER 1).
		block := bytes.Clone(buf[:readSize])
		records = append(records, block)
		readBytes += int64(readSize)

		// Count complete records in this block plus any carried partial.
		// We prepend the carried partial to the new block and count newlines.
		data := block
		if len(carried) > 0 {
			data = bytes.Join([][]byte{block, carried}, nil)
		}
		lines := bytes.Split(data, []byte{'\n'})
		// All but the last element are complete records (end with \n).
		recordCount += len(lines) - 1
		// The last element is the new carried partial (may be empty if block ends with \n).
		carried = lines[len(lines)-1]

		// Check stop condition with the count of complete records.
		if stop(recordCount) || offset == 0 {
			break
		}
	}

	// Join all blocks once (prepending block-by-block is O(n²)).
	// Records are in reverse order (last block first), so reverse them to get
	// file content in forward order (start to end).
	slices.Reverse(records)
	joined := bytes.Join(records, nil)
	lines := bytes.Split(joined, []byte{'\n'})

	// Drop everything through the first \n (the first block may be a partial record).
	firstNL := bytes.IndexByte(joined, '\n')
	if firstNL >= 0 {
		joined = joined[firstNL+1:]
		lines = bytes.Split(joined, []byte{'\n'})
	}

	// Drop the trailing fragment if the file doesn't end with a newline.
	// A session file being actively written has no trailing newline; that last
	// fragment is a partial record and must not be returned.
	if len(joined) > 0 && joined[len(joined)-1] != '\n' {
		if len(lines) > 0 {
			lines = lines[:len(lines)-1]
		}
	}

	// Lines are now in oldest-first order (file forward order after dropping header).
	// Filter empty lines.
	var out [][]byte
	for _, line := range lines {
		if len(line) > 0 {
			out = append(out, line)
		}
	}
	return out, nil
}

// ─── sessionRecord types ─────────────────────────────────────────────────

type sessionRecord struct {
	ID            string
	Header        icodingagent.SessionHeader
	Name          string
	Archived      *int64
	ModelID       string
	ModelProvider string
	Path          string
	ModTime       time.Time
	HasFile       bool
}

// sessionPath maps a session id to its transcript file in dir.
// Session files are named <timestamp>_<id>.jsonl, so filepath.Join(dir, id+".jsonl")
// never matches: the timestamp prefix is part of the name. This is a bounded suffix
// match over one already-resolved directory.
func sessionPath(dir, id string) (string, bool) {
	if !sessionIDPattern.MatchString(id) {
		return "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	suffix := "_" + id + ".jsonl"
	for _, entry := range entries {
		if name := entry.Name(); !entry.IsDir() && strings.HasSuffix(name, suffix) {
			return filepath.Join(dir, name), true
		}
	}
	return "", false
}

// sessionRecordAt reads the metadata Branch A needs from a known file.
func sessionRecordAt(path string) (sessionRecord, bool) {
	stat, err := os.Stat(path)
	if err != nil {
		return sessionRecord{}, false
	}

	f, err := os.Open(path)
	if err != nil {
		return sessionRecord{}, false
	}
	defer func() { _ = f.Close() }()

	// Bound header read at metaProbeBytes (MAJOR 4).
	reader := bufio.NewReaderSize(io.LimitReader(f, metaProbeBytes), 64*1024)

	// Read header line.
	headerLine, err := reader.ReadBytes('\n')
	if err != nil {
		return sessionRecord{}, false
	}
	headerLine = bytes.TrimSuffix(bytes.TrimSuffix(headerLine, []byte{'\n'}), []byte{'\r'})
	if len(headerLine) == 0 {
		return sessionRecord{}, false
	}

	var header icodingagent.SessionHeader
	if err := json.Unmarshal(headerLine, &header); err != nil || header.Type != "session" {
		return sessionRecord{}, false
	}

	// Read tail for session_info and session_archive.
	// The metadata read is bounded by metaProbeBytes; we just read enough records.
	records, err := walkBack(path, metaProbeBytes, func(completeRecords int) bool {
		return completeRecords >= 1000 // generous upper bound
	})
	if err != nil {
		return sessionRecord{}, false
	}

	var name string
	var archived *int64
	var modelID, modelProvider string
	// archivedSet records that a session_archive entry carrying an "archived"
	// key has been consumed, whether or not it latched a value: the writer
	// clears the stamp with a null, which latches nothing.
	var archivedSet bool
	// Last writer wins, so scan newest-first. walkBack returns oldest-first.
	for _, rec := range slices.Backward(records) {
		var base icodingagent.SessionEntryBase
		if err := json.Unmarshal(rec, &base); err != nil {
			continue
		}
		if base.Type == "session_info" && name == "" {
			var sie icodingagent.SessionInfoEntry
			if err := json.Unmarshal(rec, &sie); err == nil {
				name = sie.Name
			}
		}
		if base.Type == "model_change" && modelID == "" {
			var mce icodingagent.ModelChangeEntry
			if err := json.Unmarshal(rec, &mce); err == nil && mce.ModelID != "" {
				modelID, modelProvider = mce.ModelID, mce.Provider
			}
		}
		if base.Type == "custom" && !archivedSet {
			var ce icodingagent.CustomEntry
			if err := json.Unmarshal(rec, &ce); err == nil && ce.CustomType == "session_archive" {
				dataBytes, _ := json.Marshal(ce.Data)
				var data map[string]any
				if err := json.Unmarshal(dataBytes, &data); err == nil {
					if v, ok := data["archived"]; ok {
						archivedSet = true
						switch tv := v.(type) {
						case float64:
							i := int64(tv)
							archived = &i
						case int64:
							archived = &tv
						case int:
							i := int64(tv)
							archived = &i
						}
					}
				}
			}
		}
	}

	// Title fallback: first user message (head probe) if no name found.
	if name == "" {
		headData := make([]byte, titleProbeBytes)
		n, _ := f.ReadAt(headData, 0)
		headData = headData[:n]
		lines := bytes.SplitSeq(headData, []byte{'\n'})
		for line := range lines {
			if len(line) == 0 {
				continue
			}
			var base icodingagent.SessionEntryBase
			if err := json.Unmarshal(line, &base); err != nil {
				continue
			}
			if base.Type == "message" {
				var me icodingagent.MessageEntry
				if err := json.Unmarshal(line, &me); err == nil {
					if me.Message.Role() == "user" {
						if text := firstTextBlock(me.Message); text != "" {
							if len(text) > 200 {
								text = text[:200]
							}
							name = text
							break
						}
					}
				}
			}
		}
	}

	return sessionRecord{
		ID:            header.ID,
		Header:        header,
		Name:          name,
		Archived:      archived,
		ModelID:       modelID,
		ModelProvider: modelProvider,
		Path:          path,
		ModTime:       stat.ModTime(),
		HasFile:       true,
	}, true
}

// firstTextBlock extracts the first text block from an agent message.
func firstTextBlock(msg agent.AgentMessage) string {
	if msg.User != nil {
		for _, c := range msg.User.Content {
			if tc, ok := c.(ai.TextContent); ok && tc.Text != "" {
				return tc.Text
			}
		}
	}
	if msg.Assistant != nil {
		for _, c := range msg.Assistant.Content {
			if tc, ok := c.(ai.TextContent); ok && tc.Text != "" {
				return tc.Text
			}
		}
	}
	return ""
}

// getSessionRecord is the id-addressed front door. live is the host's wrapped
// session when it owns one, and nil otherwise; it is what makes Branch B
// reachable, because Branch B reads a *coding.Session that an id cannot yield.
func getSessionRecord(dir, id string, live *codinghost.Session) (sessionRecord, bool) {
	// Branch B: live session with no file yet.
	if live != nil {
		inner := live.Inner()
		path := inner.Path()
		hasFile := false
		if _, err := os.Stat(path); err == nil {
			hasFile = true
		}

		if !hasFile {
			return sessionRecordFromLive(inner.Inner())
		}
		// Has file: fall through to Branch A using the live session's path.
		return sessionRecordAt(path)
	}

	// Branch A: not live (or live with file), read from disk.
	path, ok := sessionPath(dir, id)
	if !ok {
		return sessionRecord{}, false
	}
	return sessionRecordAt(path)
}

// sessionRecordFromLive builds a sessionRecord from a live session with no file.
func sessionRecordFromLive(sess *icodingagent.Session) (sessionRecord, bool) {
	header := sess.Header()
	name := ""
	archived := (*int64)(nil)
	// archivedSet records that a session_archive entry carrying an "archived"
	// key has been consumed, whether or not it latched a value: the writer
	// clears the stamp with a null, which latches nothing.
	archivedSet := false

	// Scan entries backwards for session_info and session_archive.
	entries := sess.Entries()
	for _, e := range slices.Backward(entries) {

		if e.Base.Type == "session_info" && name == "" {
			var sie icodingagent.SessionInfoEntry
			if err := json.Unmarshal(e.Raw(), &sie); err == nil {
				name = sie.Name
			}
		}
		if e.Base.Type == "custom" && !archivedSet {
			var ce icodingagent.CustomEntry
			if err := json.Unmarshal(e.Raw(), &ce); err == nil && ce.CustomType == "session_archive" {
				dataBytes, _ := json.Marshal(ce.Data)
				var data map[string]any
				if err := json.Unmarshal(dataBytes, &data); err == nil {
					if v, ok := data["archived"]; ok {
						archivedSet = true
						switch tv := v.(type) {
						case float64:
							i := int64(tv)
							archived = &i
						case int64:
							archived = &tv
						case int:
							i := int64(tv)
							archived = &i
						}
					}
				}
			}
		}
		if name != "" && archivedSet {
			break
		}
	}

	// Title fallback.
	if name == "" {
		for _, e := range entries {
			if e.Base.Type == "message" {
				var me icodingagent.MessageEntry
				if err := json.Unmarshal(e.Raw(), &me); err == nil {
					if me.Message.Role() == "user" {
						if text := firstTextBlock(me.Message); text != "" {
							if len(text) > 200 {
								text = text[:200]
							}
							name = text
							break
						}
					}
				}
			}
		}
	}

	return sessionRecord{
		ID:       header.ID,
		Header:   header,
		Name:     name,
		Archived: archived,
		Path:     sess.Path(),
		ModTime:  time.Time{}, // zero for live with no file
		HasFile:  false,
	}, true
}

// ─── tailEntries ──────────────────────────────────────────────────────────

// tailEntries returns the last limit entries from path, oldest-first.
// Applies blob resolution to each record before decoding.
// Reads the entire file up to maxTailBytes to ensure we get the true last
// limit entries, not just the last ones within a partial read.
func tailEntries(path string, limit int) ([]icodingagent.SessionEntry, error) {
	// Read the entire file up to maxTailBytes; do not stop early.
	// We need the true last 'limit' entries, which may be spread across
	// multiple read blocks. The stop condition only limits byte count.
	records, err := walkBack(path, maxTailBytes, func(completeRecords int) bool {
		return false // never stop early; byte limit is the only bound
	})
	if err != nil {
		return nil, err
	}

	// Take the last limit records (walkBack returns oldest-first).
	if len(records) > limit {
		records = records[len(records)-limit:]
	}

	var out []icodingagent.SessionEntry
	budget := maxInlinedImageBytes

	for _, rec := range records {
		// Resolve blobs before decoding.
		resolved := sessionblob.Resolve(path, rec)
		budget -= len(resolved)
		if budget < 0 {
			// Emit a text part for the omitted image.
			// We still need to decode the base to get the entry ID.
			var base icodingagent.SessionEntryBase
			if err := json.Unmarshal(resolved, &base); err == nil {
				out = append(out, icodingagent.NewSessionEntry(
					json.RawMessage(`{"type":"message","id":"`+base.ID+`","parentId":null,"timestamp":"`+base.Timestamp+`","message":{"role":"assistant","content":[{"type":"text","text":"[image omitted: budget exceeded]"}]}}`),
					base,
				))
			}
			continue
		}

		var base icodingagent.SessionEntryBase
		if err := json.Unmarshal(resolved, &base); err != nil {
			continue // skip malformed
		}
		out = append(out, icodingagent.NewSessionEntry(resolved, base))
	}
	return out, nil
}

const maxInlinedImageBytes = 8 << 20

// ─── workingTreePaths ─────────────────────────────────────────────────────

// workingTreePaths runs `git -C <root> status --porcelain=v2 -z` and parses the
// NUL-separated output. Returns a slice of root-relative paths.
func workingTreePaths(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain=v2", "-z")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Read all data and split on NUL.
	// The 500-entry cap bounds the result; no byte limit needed.
	output, err := io.ReadAll(stdout)
	if err != nil {
		_ = cmd.Wait()
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 0 {
			return []string{}, nil
		}
		return nil, err
	}

	// Split on NUL, drop trailing empty record.
	records := bytes.Split(output, []byte{'\000'})
	if len(records) > 0 && len(records[len(records)-1]) == 0 {
		records = records[:len(records)-1]
	}

	var paths []string
	skipNext := false
	for _, rec := range records {
		if skipNext {
			skipNext = false
			continue
		}
		if len(rec) == 0 {
			continue
		}
		first := rec[0]
		var path string
		switch first {
		case '1', 'u':
			// Format: 1 XY <sub> <mH> <mI> <mW> <hH> <hI> <path>
			//         u XY <sub> <mH> <mI> <mW> <hH> <hI> <path>
			// 9 fields total, 8 spaces before path.
			spaceCount := 0
			pathStart := -1
			for i := 1; i < len(rec); i++ {
				if rec[i] == ' ' {
					spaceCount++
					if spaceCount == 8 {
						pathStart = i + 1
						break
					}
				}
			}
			if pathStart >= 0 && pathStart < len(rec) {
				path = string(rec[pathStart:])
			}
		case '2':
			// Format: 2 XY <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>\t<origPath>
			// 10 fields, 9 spaces before path.
			spaceCount := 0
			pathStart := -1
			for i := 1; i < len(rec); i++ {
				if rec[i] == ' ' {
					spaceCount++
					if spaceCount == 9 {
						pathStart = i + 1
						break
					}
				}
			}
			if pathStart >= 0 && pathStart < len(rec) {
				path = string(rec[pathStart:])
				// Path is followed by TAB and origPath; take only the destination path.
				if tabIdx := bytes.IndexByte([]byte(path), '\t'); tabIdx >= 0 {
					path = path[:tabIdx]
				}
			}
			// Skip the next record (original path for rename).
			skipNext = true
		case '?':
			// Format: "? <path>"
			if len(rec) > 2 && rec[1] == ' ' {
				path = string(rec[2:])
			}
		default:
			continue
		}
		if path != "" {
			paths = append(paths, path)
			if len(paths) >= maxFileStatusEntries {
				break
			}
		}
	}

	return paths, nil
}
