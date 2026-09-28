package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for publication boundaries; no network or real home access.

func TestCommittedScanCannotBeCleanedByWorkingTreeEdits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	fixture := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("imla"+"dris"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "fixture.txt")
	git("-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	if err := os.WriteFile(fixture, []byte("public"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run(dir, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("working scan exit %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(dir, []string{"-ref", "HEAD"}, &stdout, &stderr); code != 1 {
		t.Fatalf("committed scan exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "fixture.txt:1: private infrastructure") {
		t.Fatalf("committed scan output = %q", stderr.String())
	}
}

func TestPrivateContentsInTextAndBinary(t *testing.T) {
	for _, secret := range []string{
		"/home/" + "kin" + "sy/work", "/Users/" + "kin" + "sy/work",
		"imla" + "dris", "wopr" + "-staging", "node.hpe" + "corp.net",
		"$HOME/wopr" + "-lanes", ".dev" + "cache/scratch/key",
	} {
		for _, prefix := range []string{"", "\x00\xff"} {
			if len(findings("fixture.bin", []byte(prefix+secret))) == 0 {
				t.Errorf("%q with prefix %q: no finding", secret, prefix)
			}
		}
	}
}
