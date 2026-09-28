package codingagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/internal/codingagent/releasetest"
)

func releaseSource(srv *releasetest.Server) ReleaseSource {
	return ReleaseSource{BaseURL: srv.URL, Repo: releasetest.Repo, PublicKey: srv.PublicKey, Client: srv.Client()}
}

func writeOldExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "wopr")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func requireExe(t *testing.T, exe, want string) {
	t.Helper()
	if got, err := os.ReadFile(exe); err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", exe, got, err, want)
	}
}

func TestSelfUpdateInstallsVerifiedRelease(t *testing.T) {
	requireStandaloneSelfUpdateTier(t)
	srv := releasetest.New(t, "1.4.2", []byte("#!/bin/sh\necho new\n"))
	source := releaseSource(srv)
	exe := writeOldExe(t)

	archive, err := source.ResolveArchive(context.Background(), "1.4.2")
	if err != nil {
		t.Fatal(err)
	}
	if archive.Name != releasetest.ArchiveName("1.4.2") {
		t.Fatalf("archive = %s, want %s", archive.Name, releasetest.ArchiveName("1.4.2"))
	}
	if err := source.InstallArchive(context.Background(), archive, exe); err != nil {
		t.Fatal(err)
	}
	requireExe(t, exe, "#!/bin/sh\necho new\n")
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755", info.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".wopr-update*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}
}

func TestSelfUpdateRejectsBadSignature(t *testing.T) {
	srv := releasetest.New(t, "1.4.2", []byte("new"))
	source := releaseSource(srv)

	// A signature from another key.
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	srv.SetAsset("SHA256SUMS.sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(otherKey, srv.Asset("SHA256SUMS")))))
	if _, err := source.ResolveArchive(context.Background(), "1.4.2"); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("foreign signature: err = %v", err)
	}

	// SHA256SUMS altered after signing.
	srv.SetChecksums(srv.Asset("SHA256SUMS"))
	srv.SetAsset("SHA256SUMS", append(srv.Asset("SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  extra\n")...))
	if _, err := source.ResolveArchive(context.Background(), "1.4.2"); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("tampered SHA256SUMS: err = %v", err)
	}

	// Garbage signature.
	srv.SetAsset("SHA256SUMS.sig", []byte("not base64"))
	if _, err := source.ResolveArchive(context.Background(), "1.4.2"); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("malformed signature: err = %v", err)
	}
}

func TestSelfUpdateRejectsBadHash(t *testing.T) {
	requireStandaloneSelfUpdateTier(t)
	srv := releasetest.New(t, "1.4.2", []byte("new"))
	source := releaseSource(srv)
	exe := writeOldExe(t)
	archive, err := source.ResolveArchive(context.Background(), "1.4.2")
	if err != nil {
		t.Fatal(err)
	}
	// The archive is swapped after SHA256SUMS was signed.
	name := releasetest.ArchiveName("1.4.2")
	srv.SetAsset(name, releasetest.TarGz(t, releasetest.ArchiveDir("1.4.2")+"/wopr", []byte("evil")))
	if err := source.InstallArchive(context.Background(), archive, exe); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	requireExe(t, exe, "old")
}

func TestSelfUpdatePlaceholderKeyFailsClosed(t *testing.T) {
	srv := releasetest.New(t, "1.4.2", []byte("new"))
	source := releaseSource(srv)
	source.PublicKey = ReleaseSigningPublicKey
	if ReleaseSigningPublicKey != "" {
		t.Skip("a real release signing key is configured")
	}
	_, err := source.ResolveArchive(context.Background(), "1.4.2")
	if err == nil || !strings.Contains(err.Error(), "release signing key not configured") {
		t.Fatalf("err = %v, want release signing key not configured", err)
	}
	for _, path := range srv.Requests() {
		if strings.Contains(path, "/objects/") {
			t.Fatalf("downloaded %s before the key check", path)
		}
	}
}

func TestSelfUpdateRefusesPlainHTTP(t *testing.T) {
	source := ReleaseSource{BaseURL: "http://127.0.0.1:1", Repo: releasetest.Repo, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
	if _, err := source.ResolveArchive(context.Background(), "1.4.2"); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("err = %v, want an HTTPS refusal", err)
	}
}
