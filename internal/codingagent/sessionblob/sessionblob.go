// Package sessionblob keeps session images out of the session JSONL. Each
// image's bytes are written once to a content-addressed store next to the
// session files (blobs/<sha256>.<ext>, shared by every session in the
// directory), and the JSONL line keeps {"type":"image","mimeType":…,
// "blob":"<sha256>"} in place of the base64 data. Loading resolves the
// references, so everything above the file sees the same lines as before.
package sessionblob

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DirName is the blob directory beside the session files.
const DirName = "blobs"

// Dir is the blob directory for the session file at sessionPath.
func Dir(sessionPath string) string { return filepath.Join(filepath.Dir(sessionPath), DirName) }

var (
	imageMarker = []byte(`"image"`)
	blobMarker  = []byte(`"blob"`)
)

// Externalize writes the images in one JSONL line to the blob store for
// sessionPath and returns the line with references in their place. A line
// without images, or one that can't be rewritten safely, is returned as is.
func Externalize(sessionPath string, line []byte) []byte {
	if sessionPath == "" || !bytes.Contains(line, imageMarker) {
		return line
	}
	return rewrite(line, func(image map[string]any) any {
		data, ok := image["data"].(string)
		if !ok || data == "" {
			return nil
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		// Only data that re-encodes to the same text round-trips exactly.
		if err != nil || base64.StdEncoding.EncodeToString(raw) != data {
			return nil
		}
		sum := sha256.Sum256(raw)
		id := hex.EncodeToString(sum[:])
		mime, _ := image["mimeType"].(string)
		if write(Dir(sessionPath), id, ext(mime), raw) != nil {
			return nil
		}
		out := map[string]any{}
		for k, v := range image {
			if k != "data" {
				out[k] = v
			}
		}
		out["blob"] = id
		return out
	})
}

// Resolve puts the image data back into a JSONL line read from the session
// at sessionPath. A reference whose blob is gone becomes a text block
// "[image missing: <sha256>]" so the session still loads.
func Resolve(sessionPath string, line []byte) []byte {
	if !bytes.Contains(line, blobMarker) {
		return line
	}
	return rewrite(line, func(image map[string]any) any {
		id, ok := image["blob"].(string)
		if !ok || id == "" {
			return nil
		}
		mime, _ := image["mimeType"].(string)
		raw, err := os.ReadFile(filepath.Join(Dir(sessionPath), id+"."+ext(mime)))
		if err != nil {
			return map[string]any{"type": "text", "text": "[image missing: " + id + "]"}
		}
		out := map[string]any{}
		for k, v := range image {
			if k != "blob" {
				out[k] = v
			}
		}
		out["data"] = base64.StdEncoding.EncodeToString(raw)
		return out
	})
}

// ResolveFile resolves every line of a whole session file's contents.
func ResolveFile(sessionPath string, data []byte) []byte {
	if !bytes.Contains(data, blobMarker) {
		return data
	}
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		lines[i] = Resolve(sessionPath, line)
	}
	return bytes.Join(lines, []byte("\n"))
}

// Relocate copies the session file at src to dst, moving its images into
// dst's blob store, so a session copied from another directory keeps them.
func Relocate(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		lines[i] = Externalize(dst, Resolve(src, line))
	}
	return os.WriteFile(dst, bytes.Join(lines, []byte("\n")), 0o644)
}

var blobRef = regexp.MustCompile(`"blob":"([0-9a-f]{64})"`)

// Sweep removes blobs in sessionDir that no session file there references.
// Blobs younger than minAge are kept, so a session that has just written
// an image but not yet its line keeps it.
func Sweep(sessionDir string, minAge time.Duration) error {
	blobs, err := os.ReadDir(filepath.Join(sessionDir, DirName))
	if errors.Is(err, fs.ErrNotExist) || len(blobs) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	sessions, err := os.ReadDir(sessionDir)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, entry := range sessions {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			// An unreadable session might hold references: keep everything.
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 64<<20)
		for scanner.Scan() {
			for _, m := range blobRef.FindAllSubmatch(scanner.Bytes(), -1) {
				used[string(m[1])] = true
			}
		}
		scanErr := scanner.Err()
		_ = f.Close()
		if scanErr != nil {
			return scanErr
		}
	}
	for _, blob := range blobs {
		id, _, _ := strings.Cut(blob.Name(), ".")
		if used[id] || blob.IsDir() || blob.Name() == ".swept" {
			continue
		}
		info, err := blob.Info()
		if err != nil || time.Since(info.ModTime()) < minAge {
			continue
		}
		_ = os.Remove(filepath.Join(sessionDir, DirName, blob.Name()))
	}
	return nil
}

// SweepDaily runs Sweep in the background at most once a day per directory.
func SweepDaily(sessionDir string) {
	marker := filepath.Join(sessionDir, DirName, ".swept")
	if _, err := os.Stat(filepath.Dir(marker)); err != nil {
		return
	}
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		return
	}
	_ = os.WriteFile(marker, nil, 0o600)
	go func() { _ = Sweep(sessionDir, time.Hour) }()
}

// rewrite applies fn to every {"type":"image"} object in line; fn returns
// the replacement, or nil to keep the object. Numbers keep their text.
func rewrite(line []byte, fn func(map[string]any) any) []byte {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var root any
	if dec.Decode(&root) != nil {
		return line
	}
	changed := false
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			if t["type"] == "image" {
				if repl := fn(t); repl != nil {
					changed = true
					return repl
				}
			}
			for k, child := range t {
				t[k] = walk(child)
			}
		case []any:
			for i, child := range t {
				t[i] = walk(child)
			}
		}
		return v
	}
	root = walk(root)
	if !changed {
		return line
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(root) != nil {
		return line
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// write stores a blob atomically unless it is already there.
func write(dir, id, extension string, raw []byte) error {
	path := filepath.Join(dir, id+"."+extension)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+id)
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func ext(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return "bin"
}
