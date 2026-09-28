package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenSignVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "release.key")
	var out bytes.Buffer
	if err := run([]string{"gen", "-out", keyPath}, &out); err != nil {
		t.Fatal(err)
	}
	_, public, ok := strings.Cut(strings.TrimSpace(out.String()), "public key: ")
	if !ok {
		t.Fatalf("gen output = %q", out.String())
	}
	if info, _ := os.Stat(keyPath); runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("key mode = %o, want owner-only", info.Mode().Perm())
	}
	if err := run([]string{"gen", "-out", keyPath}, &out); err == nil {
		t.Fatal("gen overwrote an existing key")
	}
	private, _ := os.ReadFile(keyPath)
	t.Setenv(keyEnv, string(private))

	var pub bytes.Buffer
	if err := run([]string{"pubkey"}, &pub); err != nil || strings.TrimSpace(pub.String()) != public {
		t.Fatalf("pubkey = %q, %v; want %q", pub.String(), err, public)
	}

	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("abc  wopr.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", sums}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "-pub", public, sums}, &out); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := os.WriteFile(sums, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "-pub", public, sums}, &out); err == nil {
		t.Fatal("verify accepted a tampered file")
	}
}

func TestVerifyWithPlaceholderKeyFails(t *testing.T) {
	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	_ = os.WriteFile(sums, []byte("x"), 0o644)
	_ = os.WriteFile(sums+".sig", []byte("x"), 0o644)
	if err := run([]string{"verify", "-pub", "", sums}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("err = %v, want a placeholder error", err)
	}
}

func TestSignRequiresKey(t *testing.T) {
	t.Setenv(keyEnv, "")
	if err := run([]string{"sign", "x"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), keyEnv) {
		t.Fatalf("err = %v", err)
	}
}
