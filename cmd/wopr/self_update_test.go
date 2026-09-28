package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/releasetest"
)

// useRelease points `wopr update` at a fake GitHub release signed with the
// server's test key.
func useRelease(t *testing.T, srv *releasetest.Server) {
	t.Helper()
	previous := newReleaseSource
	t.Cleanup(func() { newReleaseSource = previous })
	newReleaseSource = func() codingagent.ReleaseSource {
		return codingagent.ReleaseSource{BaseURL: srv.URL, Repo: releasetest.Repo, PublicKey: srv.PublicKey, Client: srv.Client()}
	}
}

func requireStandaloneSelfUpdateTier(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a standalone Windows binary is not replaced in place")
	}
}

func downloadedArchive(srv *releasetest.Server) bool {
	for _, path := range srv.Requests() {
		if strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".zip") {
			return true
		}
	}
	return false
}

// The version check runs before the tier, so a current install exits 0 even
// where wopr could not replace it.
func TestSelfUpdateUpToDateExitsZeroBeforeTheTier(t *testing.T) {
	t.Setenv("WOPR_INSTALL_TIER", "container")
	useRelease(t, releasetest.New(t, Version, []byte("same")))
	_, stderr, code := captureStdoutStderr(t, func() int { return runSelfUpdate(false) })
	if code != 0 || !strings.Contains(stderr, "is up to date") {
		t.Fatalf("code = %d, stderr = %q; want 0 and up to date", code, stderr)
	}
}

func TestSelfUpdateRefusingTiersNeverDownload(t *testing.T) {
	for tier, want := range map[string]string{"container": "re-deploy", "immutable-binary": "cannot be updated in place"} {
		t.Run(tier, func(t *testing.T) {
			t.Setenv("WOPR_INSTALL_TIER", tier)
			srv := releasetest.New(t, "99.0.0", []byte("new"))
			useRelease(t, srv)
			stdout, stderr, code := captureStdoutStderr(t, func() int { return runSelfUpdate(false) })
			if code != 1 || stdout != "" || !strings.Contains(stderr, want) {
				t.Fatalf("code = %d, stdout = %q, stderr = %q; want 1 and %q", code, stdout, stderr, want)
			}
			if downloadedArchive(srv) {
				t.Fatal("a refusing tier downloaded the release archive")
			}
		})
	}
}

func TestSelfUpdateUnreachableGitHubIsActionable(t *testing.T) {
	srv := releasetest.New(t, "99.0.0", []byte("new"))
	useRelease(t, srv)
	srv.Close()
	_, stderr, code := captureStdoutStderr(t, func() int { return runSelfUpdate(false) })
	if code != 1 || !strings.Contains(stderr, "could not resolve the latest wopr release") || !strings.Contains(stderr, codingagent.ReleasesURL) {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestApplyStandaloneUpdateReplacesTheExecutable(t *testing.T) {
	requireStandaloneSelfUpdateTier(t)
	srv := releasetest.New(t, "99.0.0", []byte("#!/bin/sh\necho updated\n"))
	useRelease(t, srv)
	exe := filepath.Join(t.TempDir(), "wopr")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, _ := captureStdoutStderr(t, func() int {
		if err := applyStandaloneUpdate(context.Background(), newReleaseSource(), "99.0.0", exe); err != nil {
			t.Errorf("applyStandaloneUpdate: %v", err)
		}
		return 0
	})
	if got, _ := os.ReadFile(exe); string(got) != "#!/bin/sh\necho updated\n" {
		t.Fatalf("executable = %q, want the release binary", got)
	}
	if !strings.Contains(stderr, "Updated to wopr 99.0.0") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestApplyStandaloneUpdateWithPlaceholderKeyFailsClosed(t *testing.T) {
	if codingagent.ReleaseSigningPublicKey != "" {
		t.Skip("a real release signing key is configured")
	}
	srv := releasetest.New(t, "99.0.0", []byte("new"))
	source := codingagent.DefaultReleaseSource(srv.Client())
	source.BaseURL = srv.URL
	exe := filepath.Join(t.TempDir(), "wopr")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := applyStandaloneUpdate(context.Background(), source, "99.0.0", exe)
	if err == nil || !strings.Contains(err.Error(), "release signing key not configured") {
		t.Fatalf("err = %v, want release signing key not configured", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("executable changed: %q", got)
	}
}
