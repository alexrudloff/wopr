package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runVerify(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	if os.Getenv("WOPR_HOME") == "" {
		t.Setenv("WOPR_HOME", t.TempDir())
	}
	var stdout, stderr bytes.Buffer
	code := runVerifyCommand(append([]string{"verify"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVerifyReportsTheBinaryDigestAndIdentity(t *testing.T) {
	code, out, errOut := runVerify(t, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var report verifyReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if !report.Verified || report.Identity.Version == "" || len(report.Targets) != 1 || report.Targets[0].Kind != "binary" || len(report.Targets[0].SHA256) != 64 {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyChecksumsRequireAListedMatchingDigest(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "wopr-1.0.0-linux-amd64.tar.gz")
	if err := os.WriteFile(artifact, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(artifact)
	if err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	write := func(content string) {
		if err := os.WriteFile(sums, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(digest + "  wopr-1.0.0-linux-amd64.tar.gz\n")
	if code, out, _ := runVerify(t, "--checksums", sums, artifact); code != 0 || !strings.Contains(out, "ok   checksum") {
		t.Fatalf("matching digest: exit %d\n%s", code, out)
	}
	write(strings.Repeat("0", 64) + "  wopr-1.0.0-linux-amd64.tar.gz\n")
	if code, out, _ := runVerify(t, "--checksums", sums, artifact); code != 1 || !strings.Contains(out, "digest differs") {
		t.Fatalf("mismatched digest: exit %d\n%s", code, out)
	}
	write(digest + "  some-other-file\n")
	if code, out, _ := runVerify(t, "--checksums", sums, artifact); code != 1 || !strings.Contains(out, "is not listed") {
		t.Fatalf("unlisted file: exit %d\n%s", code, out)
	}
	write("not a checksum line\n")
	if code, _, errOut := runVerify(t, "--checksums", sums, artifact); code != 1 || !strings.Contains(errOut, "not a SHA-256 checksum line") {
		t.Fatalf("malformed checksums: exit %d\n%s", code, errOut)
	}
}

func TestVerifyProvenanceWithoutGhNamesTheExactCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	artifact := filepath.Join(t.TempDir(), "wopr")
	if err := os.WriteFile(artifact, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runVerify(t, "--provenance", artifact)
	if code != 0 || !strings.Contains(out, "n/a  provenance") || !strings.Contains(out, "gh attestation verify "+artifact+" --repo alexrudloff/wopr --signer-workflow alexrudloff/wopr/.github/workflows/release.yml") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, _, _ := runVerify(t, "--repo"); code != 2 {
		t.Fatalf("--repo without a value exit = %d, want 2", code)
	}
	workflow := "acme/reviewer/.github/workflows/release.yml"
	code, out, _ = runVerify(t, "--provenance", "--repo", "acme/reviewer", "--signer-workflow", workflow, artifact)
	if code != 0 || !strings.Contains(out, "--signer-workflow "+workflow) {
		t.Fatalf("custom signer workflow: exit %d\n%s", code, out)
	}
}
