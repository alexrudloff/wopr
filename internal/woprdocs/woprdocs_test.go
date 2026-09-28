package woprdocs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "../etc/passwd", "/etc/passwd", "foo/bar.md", "..\\evil.md"} {
		if _, err := Read(bad); err == nil {
			t.Errorf("Read(%q) should reject", bad)
		}
	}
}

func TestEnsureSyncedReplacesOlderDocs(t *testing.T) {
	root := t.TempDir()
	dir := DocsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, markerFile), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quickstart.md"), []byte("stale quickstart page\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureSynced(root); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(dir, "quickstart.md"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(page, []byte("stale quickstart page")) || !bytes.Contains(page, []byte("# Quickstart")) {
		t.Fatalf("quickstart was not replaced by the embedded page:\n%s", page)
	}
	version, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := contentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(version)) != digest {
		t.Fatalf("marker = %q, want %q", version, digest)
	}
}
