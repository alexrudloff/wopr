// Package releasetest serves a fake GitHub release of wopr for self-update
// tests: the releases/latest redirect, a signed SHA256SUMS, and this
// platform's archive, with each asset download redirected to an object store
// the way github.com does.
package releasetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Repo is the owner/name the fake server publishes under.
const Repo = "alexrudloff/wopr"

// Server is a fake github.com for one wopr release.
type Server struct {
	*httptest.Server
	Version    string
	PrivateKey ed25519.PrivateKey
	// PublicKey is the base64 key that verifies SHA256SUMS.sig.
	PublicKey string

	mu       sync.Mutex
	assets   map[string][]byte
	requests []string
}

// ArchiveDir is the directory the platform archive unpacks to.
func ArchiveDir(version string) string {
	return fmt.Sprintf("wopr-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
}

// ArchiveName is the platform archive's asset name.
func ArchiveName(version string) string {
	if runtime.GOOS == "windows" {
		return ArchiveDir(version) + ".zip"
	}
	return ArchiveDir(version) + ".tar.gz"
}

// New publishes version with binary as the platform's wopr executable.
func New(t testing.TB, version string, binary []byte) *Server {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Version:    version,
		PrivateKey: private,
		PublicKey:  base64.StdEncoding.EncodeToString(public),
		assets:     map[string][]byte{},
	}
	s.assets[ArchiveName(version)] = TarGz(t, ArchiveDir(version)+"/wopr", binary)
	s.SetChecksums(Checksums(s.assets))
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// TarGz builds a gzip tar holding one executable regular file at member.
func TarGz(t testing.TB, member string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
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

// Checksums renders sha256sum output for assets.
func Checksums(assets map[string][]byte) []byte {
	var b strings.Builder
	for name, data := range assets {
		if name == "SHA256SUMS" || name == "SHA256SUMS.sig" {
			continue
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return []byte(b.String())
}

// SetChecksums replaces SHA256SUMS and signs it with the server's key.
func (s *Server) SetChecksums(sums []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets["SHA256SUMS"] = sums
	s.assets["SHA256SUMS.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(s.PrivateKey, sums)) + "\n")
}

// SetAsset replaces (or, with nil, removes) one asset without re-signing.
func (s *Server) SetAsset(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data == nil {
		delete(s.assets, name)
		return
	}
	s.assets[name] = data
}

// Asset returns a published asset.
func (s *Server) Asset(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assets[name]
}

// Requests lists the request paths served so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	s.mu.Unlock()
	downloads := "/" + Repo + "/releases/download/v" + s.Version + "/"
	switch {
	case r.URL.Path == "/"+Repo+"/releases/latest":
		http.Redirect(w, r, s.URL+"/"+Repo+"/releases/tag/v"+s.Version, http.StatusFound)
	case strings.HasPrefix(r.URL.Path, downloads):
		http.Redirect(w, r, "/objects/"+strings.TrimPrefix(r.URL.Path, downloads), http.StatusFound) //nolint:gosec // G710: a test server redirecting to its own object path, as github.com does.
	case strings.HasPrefix(r.URL.Path, "/objects/"):
		data := s.Asset(strings.TrimPrefix(r.URL.Path, "/objects/"))
		if data == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}
