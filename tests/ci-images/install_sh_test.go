package ciimages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/internal/testenv"
)

// These tests run install.sh (served from
// https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh) offline:
// a fake curl on PATH serves fixture files for the GitHub release URLs, so
// every download, checksum, and install path runs for real against a
// temporary HOME.

const (
	installVersion  = "0.2.0"
	installReleases = "https://github.com/alexrudloff/wopr/releases"
)

// fakeCurl answers `curl ... [-o FILE] URL` from $FIXTURES/<url without
// scheme>, exiting 22 (curl's HTTP error status under -f) when no fixture
// exists. With -w (the releases/latest redirect lookup) the fixture holds
// the redirect target, which it prints.
const fakeCurl = `#!/bin/sh
out=""
url=""
write=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -w) write=$2; shift 2 ;;
    --proto | --retry) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
echo "$url" >> "$FIXTURES/.requests"
file="$FIXTURES/${url#https://}"
[ -f "$file" ] || exit 22
if [ -n "$write" ]; then cat "$file"; elif [ -n "$out" ]; then cp "$file" "$out"; else cat "$file"; fi
`

type installFixture struct {
	root, home, installDir, release, archive, archiveSHA string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func installArchiveName() (name, archive string) {
	goos, arch := "linux", "amd64"
	if runtime.GOOS == "darwin" {
		goos = "darwin"
	}
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	name = "wopr-" + installVersion + "-" + goos + "-" + arch
	return name, name + ".tar.gz"
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

// releaseArchive builds <name>/wopr, a script printing the release version.
func releaseArchive(t *testing.T, name string) []byte {
	t.Helper()
	wopr := []byte("#!/bin/sh\necho \"" + installVersion + "\"\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, header := range []*tar.Header{
		{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: name + "/wopr", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(wopr))},
	} {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tw.Write(wopr); err != nil {
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

func newInstallFixture(t *testing.T) installFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh installs Linux and macOS releases")
	}
	root := t.TempDir()
	name, archive := installArchiveName()
	fixtures := filepath.Join(root, "fixtures")
	release := filepath.Join(fixtures, strings.TrimPrefix(installReleases, "https://"), "download", "v"+installVersion)
	data := releaseArchive(t, name)
	sum := sha256.Sum256(data)
	f := installFixture{
		root:       root,
		home:       filepath.Join(root, "home"),
		installDir: filepath.Join(root, "home", "bin"),
		release:    release,
		archive:    archive,
		archiveSHA: hex.EncodeToString(sum[:]),
	}
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "fakebin", "curl"), []byte(fakeCurl), 0o755)
	writeFile(t, filepath.Join(release, archive), data, 0o644)
	writeFile(t, filepath.Join(fixtures, strings.TrimPrefix(installReleases, "https://"), "latest"), []byte(installReleases+"/tag/v"+installVersion), 0o644)
	f.writeSums(t, strings.Repeat("0", 64)+"  ./evidence/sbom.spdx.json", f.archiveSHA+"  ./"+archive)
	return f
}

func (f installFixture) writeSums(t *testing.T, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(f.release, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

type installResult struct {
	status         int
	stdout, stderr string
}

// run executes install.sh with only the fixture environment, as a user's
// `curl ... | sh` would, never the test process's own.
func (f installFixture) run(t *testing.T, extraEnv ...string) installResult {
	t.Helper()
	cmd := exec.Command(testenv.Sh(t), filepath.Join(repoRoot(t), "install.sh"))
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(f.root, "fakebin") + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + f.home,
		"FIXTURES=" + filepath.Join(f.root, "fixtures"),
		"WOPR_INSTALL_DIR=" + f.installDir,
	}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	status := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		status = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run install.sh: %v", err)
	}
	return installResult{status: status, stdout: stdout.String(), stderr: stderr.String()}
}

func (f installFixture) assertNothingInstalled(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.installDir); !os.IsNotExist(err) {
		t.Fatalf("install directory exists after a refused install: %v", err)
	}
}

func assertFailure(t *testing.T, got installResult, want string) {
	t.Helper()
	if got.status != 1 {
		t.Fatalf("status %d, want 1\nstdout:\n%s\nstderr:\n%s", got.status, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, want) {
		t.Fatalf("stderr does not report %q:\n%s", want, got.stderr)
	}
}

func TestInstallShInstallsTheLatestReleaseAfterVerifyingItsSHA256(t *testing.T) {
	f := newInstallFixture(t)
	got := f.run(t)
	if got.status != 0 {
		t.Fatalf("status %d:\n%s", got.status, got.stderr)
	}
	if !strings.Contains(got.stdout, "Verified SHA-256 "+f.archiveSHA) {
		t.Fatalf("stdout does not report the verified digest:\n%s", got.stdout)
	}
	want := installVersion
	if !regexp.MustCompile(`Installed ` + regexp.QuoteMeta(want) + ` to `).MatchString(got.stdout) {
		t.Fatalf("stdout does not report the installed version %s:\n%s", want, got.stdout)
	}
	out, err := exec.Command(filepath.Join(f.installDir, "wopr"), "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("installed wopr --version = %q, want %q", out, want)
	}
}

func TestInstallShRefusesAnArchiveWhoseChecksumDoesNotMatch(t *testing.T) {
	f := newInstallFixture(t)
	f.writeSums(t, strings.Repeat("a", 64)+"  ./"+f.archive)
	assertFailure(t, f.run(t), "checksum mismatch")
	f.assertNothingInstalled(t)
}

func TestInstallShRefusesAReleaseWithoutOrWithAmbiguousSHA256SUMS(t *testing.T) {
	missing := newInstallFixture(t)
	if err := os.Remove(filepath.Join(missing.release, "SHA256SUMS")); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, missing.run(t), "has no SHA256SUMS")
	missing.assertNothingInstalled(t)

	doubled := newInstallFixture(t)
	doubled.writeSums(t, doubled.archiveSHA+"  ./"+doubled.archive, doubled.archiveSHA+"  "+doubled.archive)
	assertFailure(t, doubled.run(t), "no single valid entry")
}
