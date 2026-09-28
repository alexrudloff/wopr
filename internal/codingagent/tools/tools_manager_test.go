package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildTarGz(t *testing.T, relPath string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     relPath,
		Mode:     0o755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, relPath string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(relPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractZip_PathTraversalRejected(t *testing.T) {
	asset := buildZip(t, "../../etc/evil", []byte("nope"))
	tmpArchive := filepath.Join(t.TempDir(), "evil.zip")
	if err := os.WriteFile(tmpArchive, asset, 0o644); err != nil {
		t.Fatal(err)
	}
	extractDir := t.TempDir()
	err := extractZip(tmpArchive, extractDir)
	if err == nil {
		t.Fatal("expected error for path-traversing zip entry")
	}
	if !strings.Contains(err.Error(), "escapes extract dir") {
		t.Errorf("error %q does not name the violation", err)
	}
}

func TestExtractTarGz_PathTraversalRejected(t *testing.T) {
	asset := buildTarGz(t, "../../etc/evil", []byte("nope"))
	tmpArchive := filepath.Join(t.TempDir(), "evil.tar.gz")
	if err := os.WriteFile(tmpArchive, asset, 0o644); err != nil {
		t.Fatal(err)
	}
	extractDir := t.TempDir()
	err := extractTarGz(tmpArchive, extractDir)
	if err == nil {
		t.Fatal("expected error for path-traversing tar entry")
	}
}
