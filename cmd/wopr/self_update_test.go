package main

import (
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
