package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveStartupSessionSelectionUsesSessionCWD(t *testing.T) {
	launchCWD := t.TempDir()
	targetCWD := filepath.Join(launchCWD, "target")
	if err := os.Mkdir(targetCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := writeStartupSession(t, filepath.Join(launchCWD, "sessions"), "selected", targetCWD)

	got, err := resolveStartupSessionSelection(CLIFlags{Session: sessionPath}, launchCWD, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.resumePath != sessionPath {
		t.Fatalf("resumePath = %q, want %q", got.resumePath, sessionPath)
	}
	wantTarget := canonicalStartupDir(targetCWD)
	if got.runtimeCWD != wantTarget {
		t.Fatalf("runtimeCWD = %q, want %q", got.runtimeCWD, wantTarget)
	}
	if got.sessionDir != filepath.Dir(sessionPath) {
		t.Fatalf("sessionDir = %q, want %q", got.sessionDir, filepath.Dir(sessionPath))
	}
}

func TestResolveStartupSessionSelectionFindsGlobalPrefixBeforeRuntime(t *testing.T) {
	launchCWD := t.TempDir()
	otherCWD := t.TempDir()
	sessionDir := filepath.Join(launchCWD, "sessions")
	path := writeStartupSession(t, sessionDir, "global-session-456", otherCWD)

	got, err := resolveStartupSessionSelection(CLIFlags{Session: "global-sess"}, launchCWD, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.crossProject == nil || got.crossProject.path != path || got.crossProject.cwd != otherCWD {
		t.Fatalf("crossProject = %+v, want %q from %q", got.crossProject, path, otherCWD)
	}
	if got.resumePath != "" {
		t.Fatalf("resumePath = %q, want confirmation before opening", got.resumePath)
	}
}

// Opening another project's session must be confirmed; a bare Enter is no.
func TestConfirmCrossProjectSessionDefaultsNo(t *testing.T) {
	confirmed, err := confirmCrossProjectSession(strings.NewReader("\n"), io.Discard, "/other")
	if err != nil || confirmed {
		t.Fatalf("empty answer confirmed = %v, %v", confirmed, err)
	}
}

func writeStartupSession(t *testing.T, dir, id, cwd string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	content := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":\"2026-08-04T00:00:00Z\",\"cwd\":%q}\n", id, cwd)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
