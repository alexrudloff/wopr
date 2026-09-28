package codingagent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// ReleaseRepo is the GitHub repository that publishes wopr releases.
const ReleaseRepo = "alexrudloff/wopr"

// ReleasesURL is the human-facing page listing every wopr release.
const ReleasesURL = "https://github.com/" + ReleaseRepo + "/releases"

// InstallScriptURL is the installer for macOS and Linux.
const InstallScriptURL = "https://raw.githubusercontent.com/" + ReleaseRepo + "/main/install.sh"

// Release asset names beside the archives.
const (
	releaseChecksumsName = "SHA256SUMS"
	releaseSignatureName = "SHA256SUMS.sig"
)

// maxUpdateArchiveBytes bounds a downloaded release archive and the binary
// extracted from it.
const maxUpdateArchiveBytes = 512 << 20

// maxReleaseMetadataBytes bounds SHA256SUMS and its signature.
const maxReleaseMetadataBytes = 1 << 20

// BinaryUpdate describes an available newer release.
type BinaryUpdate struct {
	CurrentVersion string
	LatestVersion  string
	Command        string // the command that applies it: "wopr update"
}

// ReleaseSource reads wopr releases from GitHub: the releases/latest redirect
// names the version, and each release carries SHA256SUMS, its Ed25519
// signature SHA256SUMS.sig, and one archive per platform.
type ReleaseSource struct {
	// BaseURL is the GitHub web origin (https://github.com). Tests point it
	// at an httptest TLS server.
	BaseURL string
	// Repo is owner/name.
	Repo string
	// PublicKey is the base64 Ed25519 key that must sign SHA256SUMS. Empty
	// fails closed.
	PublicKey string
	Client    *http.Client
	// GOOS and GOARCH select the archive; empty means the running platform.
	GOOS, GOARCH string
}

// DefaultReleaseSource reads the published wopr releases with the embedded
// release signing key.
func DefaultReleaseSource(client *http.Client) ReleaseSource {
	return ReleaseSource{
		BaseURL:   "https://github.com",
		Repo:      ReleaseRepo,
		PublicKey: ReleaseSigningPublicKey,
		Client:    client,
	}
}

func (s ReleaseSource) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s ReleaseSource) platform() (string, string) {
	goos, goarch := s.GOOS, s.GOARCH
	goos = cmp.Or(goos, runtime.GOOS)
	goarch = cmp.Or(goarch, runtime.GOARCH)
	return goos, goarch
}

// LatestVersion resolves the newest published release without the GitHub API.
func (s ReleaseSource) LatestVersion(ctx context.Context) (string, error) {
	version, err := tools.LatestReleaseVersion(ctx, s.client(), s.BaseURL, s.Repo)
	if err != nil {
		return "", err
	}
	if strictSemver(version) == "" {
		return "", fmt.Errorf("latest release tag v%s is not a release version", version)
	}
	return version, nil
}

// ReleaseArchive is a platform archive whose SHA-256 came from a verified
// SHA256SUMS.
type ReleaseArchive struct {
	Version string
	Name    string // wopr-<version>-<goos>-<goarch>.tar.gz
	Dir     string // top-level directory inside the archive
	URL     string
	SHA256  string
}

// ArchiveBaseName is the release archive name without extension, which is
// also the directory the archive unpacks to.
func ArchiveBaseName(version, goos, goarch string) string {
	return fmt.Sprintf("%s-%s-%s-%s", AppName, version, goos, goarch)
}

func (s ReleaseSource) downloadURL(version, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/v%s/%s", strings.TrimSuffix(s.BaseURL, "/"), s.Repo, version, url.PathEscape(asset))
}

// ResolveArchive downloads SHA256SUMS and SHA256SUMS.sig for version, verifies
// the signature against the release key, and returns this platform's archive
// with its expected digest. Nothing is trusted before the signature verifies.
func (s ReleaseSource) ResolveArchive(ctx context.Context, version string) (*ReleaseArchive, error) {
	publicKey, err := decodeReleasePublicKey(s.PublicKey)
	if err != nil {
		return nil, err
	}
	sums, err := s.fetchSmall(ctx, s.downloadURL(version, releaseChecksumsName))
	if err != nil {
		return nil, fmt.Errorf("release v%s: %w", version, err)
	}
	signature, err := s.fetchSmall(ctx, s.downloadURL(version, releaseSignatureName))
	if err != nil {
		return nil, fmt.Errorf("release v%s: %w", version, err)
	}
	if !verifyReleaseSignature(sums, signature, publicKey) {
		return nil, fmt.Errorf("release v%s: SHA256SUMS signature verification failed: refusing to install", version)
	}
	goos, goarch := s.platform()
	dir := ArchiveBaseName(version, goos, goarch)
	name := dir + ".tar.gz"
	if goos == "windows" {
		name = dir + ".zip"
	}
	digest, err := checksumFor(sums, name)
	if err != nil {
		return nil, fmt.Errorf("release v%s: %w", version, err)
	}
	return &ReleaseArchive{Version: version, Name: name, Dir: dir, URL: s.downloadURL(version, name), SHA256: digest}, nil
}

// errReleaseKeyNotConfigured is returned while ReleaseSigningPublicKey is
// still the placeholder.
var errReleaseKeyNotConfigured = errors.New("release signing key not configured: this wopr build cannot verify releases; " +
	"reinstall with the install script or `go install github.com/" + ReleaseRepo + "/cmd/wopr@latest`")

func decodeReleasePublicKey(encoded string) (ed25519.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errReleaseKeyNotConfigured
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release signing key is malformed: want base64 of %d bytes", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// verifyReleaseSignature reports whether signature (base64 text, as
// SHA256SUMS.sig holds it) is a valid Ed25519 signature over sums.
func verifyReleaseSignature(sums, signature []byte, key ed25519.PublicKey) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(key, sums, raw)
}

// checksumFor returns the digest SHA256SUMS lists for name, requiring exactly
// one well-formed entry.
func checksumFor(sums []byte, name string) (string, error) {
	var found []string
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		entry := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		if entry == name {
			found = append(found, strings.ToLower(fields[0]))
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("SHA256SUMS has no single entry for %s", name)
	}
	if len(found[0]) != sha256.Size*2 {
		return "", fmt.Errorf("SHA256SUMS entry for %s is not a SHA-256 digest", name)
	}
	if _, err := hex.DecodeString(found[0]); err != nil {
		return "", fmt.Errorf("SHA256SUMS entry for %s is not a SHA-256 digest", name)
	}
	return found[0], nil
}

func (s ReleaseSource) fetchSmall(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := s.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReleaseMetadataBytes {
		return nil, fmt.Errorf("%s exceeds %d-byte limit", rawURL, maxReleaseMetadataBytes)
	}
	return data, nil
}

// get fetches rawURL over HTTPS, refusing any redirect off HTTPS (GitHub
// serves release assets through a redirect to its object store).
func (s ReleaseSource) get(ctx context.Context, rawURL string) (*http.Response, error) {
	if err := requireHTTPS(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", AppName)
	client := *s.client()
	previous := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if err := requireHTTPS(next.URL.String()); err != nil {
			return err
		}
		if previous != nil {
			return previous(next, via)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}
	return resp, nil
}

func requireHTTPS(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid update URL %q", raw)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("update URL %s must use HTTPS", raw)
	}
	return nil
}

// InstallArchive downloads archive, verifies its SHA-256 against the signed
// SHA256SUMS entry, extracts the wopr binary, and atomically replaces
// exePath. Unix only: a running .exe cannot be replaced in place.
func (s ReleaseSource) InstallArchive(ctx context.Context, archive *ReleaseArchive, exePath string) error {
	if goos, _ := s.platform(); goos == "windows" || runtime.GOOS == "windows" {
		return fmt.Errorf("in-place self-update is not supported on Windows; download a new release from %s", ReleasesURL)
	}
	dir := filepath.Dir(exePath)
	archivePath, sum, err := s.downloadToFile(ctx, archive.URL, dir, maxUpdateArchiveBytes)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archivePath) }()
	if !strings.EqualFold(sum, archive.SHA256) {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", archive.Name)
	}
	binaryPath, err := extractReleaseBinary(archivePath, archive.Dir+"/"+AppName, dir)
	if err != nil {
		return fmt.Errorf("%s: %w", archive.Name, err)
	}
	defer func() { _ = os.Remove(binaryPath) }()
	if err := os.Rename(binaryPath, exePath); err != nil {
		return fmt.Errorf("replace %s: %w", exePath, err)
	}
	return nil
}

// downloadToFile streams rawURL into a temporary file in dir while hashing
// it, so memory use does not grow with the archive. The caller removes the
// returned path.
func (s ReleaseSource) downloadToFile(ctx context.Context, rawURL, dir string, limit int64) (string, string, error) {
	resp, err := s.get(ctx, rawURL)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength > limit {
		return "", "", fmt.Errorf("download %s exceeds %d-byte limit", rawURL, limit)
	}
	tmp, err := os.CreateTemp(dir, ".wopr-update-*")
	if err != nil {
		return "", "", fmt.Errorf("stage update (is %s writable?): %w", dir, err)
	}
	tmpPath := tmp.Name()
	fail := func(err error) (string, string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", "", err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fail(err)
	}
	if written > limit {
		return fail(fmt.Errorf("download %s exceeds %d-byte limit", rawURL, limit))
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fail(fmt.Errorf("download %s truncated: received %d of %d bytes", rawURL, written, resp.ContentLength))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", err
	}
	return tmpPath, hex.EncodeToString(digest.Sum(nil)), nil
}

// extractReleaseBinary copies the regular file member from the gzip tar at
// archivePath into an executable temporary file in dir.
func extractReleaseBinary(archivePath, member, dir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("read archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("archive does not contain %s", member)
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if strings.TrimPrefix(header.Name, "./") != member {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("archive member %s is not a regular file", member)
		}
		if header.Size > maxUpdateArchiveBytes {
			return "", fmt.Errorf("archive member %s exceeds %d-byte limit", member, maxUpdateArchiveBytes)
		}
		out, err := os.CreateTemp(dir, ".wopr-update-bin-*")
		if err != nil {
			return "", fmt.Errorf("stage update (is %s writable?): %w", dir, err)
		}
		outPath := out.Name()
		_, copyErr := io.Copy(out, io.LimitReader(tr, maxUpdateArchiveBytes))
		if copyErr == nil {
			copyErr = out.Chmod(0o755)
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		if closeErr := out.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = os.Remove(outPath)
			return "", copyErr
		}
		return outPath, nil
	}
}

// SelfUpdateFallback is the manual path shown when self-update cannot run.
func SelfUpdateFallback() string {
	return fmt.Sprintf("Install the latest release with `curl -fsSL %s | sh`, download it from %s, "+
		"or build it with `go install github.com/%s/cmd/%s@latest`.",
		InstallScriptURL, ReleasesURL, ReleaseRepo, AppName)
}

// CheckForBinaryUpdate reports a newer published release, or nil when up to
// date, offline, or GitHub is unreachable. It never surfaces an error: a
// startup update check is best-effort and must not disrupt the session. It
// costs one request to the releases/latest redirect.
func CheckForBinaryUpdate(ctx context.Context, source ReleaseSource, currentVersion string) *BinaryUpdate {
	// WOPR_OFFLINE suppresses bounded startup update probes. Explicit
	// `wopr update` does not use this guard: its errors must remain actionable.
	if tools.IsOfflineModeEnabled() {
		return nil
	}
	if strictSemver(strings.TrimSpace(currentVersion)) == "" {
		return nil
	}
	latest, err := source.LatestVersion(ctx)
	if err != nil || CompareVersions(currentVersion, latest) >= 0 {
		return nil
	}
	return &BinaryUpdate{CurrentVersion: currentVersion, LatestVersion: latest, Command: AppName + " update"}
}

// CompareVersions compares strict semantic versions. Returns -1 if a<b, 1 if
// a>b, and 0 when equal or when either side is malformed. Callers parsing
// release metadata must reject malformed remote versions before comparison;
// the zero fallback here keeps development/CI local versions non-disruptive.
func CompareVersions(a, b string) int {
	pa, pb := strictSemver(strings.TrimSpace(a)), strictSemver(strings.TrimSpace(b))
	if pa == "" || pb == "" {
		return 0
	}
	return semver.Compare(pa, pb)
}

// strictSemver returns version with a "v" prefix when it is a full
// MAJOR.MINOR.PATCH semantic version (no "v" prefix of its own), else "".
func strictSemver(version string) string {
	v := "v" + version
	if c := semver.Canonical(v); c == "" || (v != c && !strings.HasPrefix(v, c+"+")) {
		return ""
	}
	return v
}
