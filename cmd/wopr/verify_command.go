package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

// defaultVerifyRepo is the repository whose release workflow signs WOPR's
// provenance attestations.
const defaultVerifyRepo = "alexrudloff/wopr"

const verifyUsage = `Usage: wopr verify [--json] [--checksums FILE] [--provenance] [--repo OWNER/NAME] [--signer-workflow WORKFLOW] [path...]

Verification starts from the SHA-256 of the bytes on disk. A version string,
file name, or download URL proves nothing.

  (no path)          verify this wopr binary
  path               a file is verified by digest
  --checksums FILE   require each file's digest to match its entry in a SHA256SUMS file
  --provenance       check GitHub build provenance with ` + "`gh attestation verify`" + `,
                     signed by the selected repository workflow
  --repo OWNER/NAME  repository for --provenance (default ` + defaultVerifyRepo + `)
  --signer-workflow WORKFLOW
                     expected GitHub Actions workflow (default
                     OWNER/NAME/.github/workflows/release.yml)
  --json             print the report as JSON
`

type verifyCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok", "failed", or "unavailable"
	Detail string `json:"detail,omitempty"`
}

type verifyTarget struct {
	Path   string        `json:"path"`
	Kind   string        `json:"kind"`
	SHA256 string        `json:"sha256,omitempty"`
	Checks []verifyCheck `json:"checks"`
}

type verifyIdentity struct {
	Version  string `json:"version"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
	Build    string `json:"build"`
	Revision string `json:"revision,omitempty"`
	Modified bool   `json:"modified,omitempty"`
}

type verifyReport struct {
	Verified bool           `json:"verified"`
	Identity verifyIdentity `json:"identity"`
	Targets  []verifyTarget `json:"targets"`
}

type verifyOptions struct {
	json           bool
	checksums      string
	provenance     bool
	repo           string
	signerWorkflow string
	paths          []string
}

// runVerifyCommand handles `wopr verify`. It reports -1 for other commands.
func runVerifyCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "verify" {
		return -1
	}
	opts := verifyOptions{repo: defaultVerifyRepo}
	for i := 1; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--json":
			opts.json = true
		case "--provenance":
			opts.provenance = true
		case "--checksums", "--repo", "--signer-workflow":
			if i+1 >= len(args) {
				_, _ = fmt.Fprintf(stderr, "wopr verify: %s requires a value\n%s", arg, verifyUsage)
				return 2
			}
			i++
			switch arg {
			case "--checksums":
				opts.checksums = args[i]
			case "--repo":
				opts.repo = args[i]
			case "--signer-workflow":
				opts.signerWorkflow = args[i]
			}
		case "-h", "--help":
			_, _ = io.WriteString(stdout, verifyUsage)
			return 0
		default:
			if strings.HasPrefix(arg, "-") {
				_, _ = fmt.Fprintf(stderr, "wopr verify: unknown option %q\n%s", arg, verifyUsage)
				return 2
			}
			opts.paths = append(opts.paths, arg)
		}
	}
	self, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "wopr verify: locate this binary: %v\n", err)
		return 1
	}
	sums, err := readChecksums(opts.checksums)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "wopr verify: %v\n", err)
		return 1
	}
	report := verifyReport{Identity: binaryIdentity()}
	if len(opts.paths) == 0 {
		report.Targets = append(report.Targets, verifyFile(self, "binary", sums, opts))
	}
	for _, path := range opts.paths {
		report.Targets = append(report.Targets, verifyPath(path, sums, opts))
	}
	report.Verified = true
	for _, target := range report.Targets {
		for _, check := range target.Checks {
			if check.Status == "failed" {
				report.Verified = false
			}
		}
	}
	if opts.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(report)
	} else {
		writeVerifyText(stdout, report)
	}
	if !report.Verified {
		return 1
	}
	return 0
}

func binaryIdentity() verifyIdentity {
	identity := verifyIdentity{
		Version: Version, Go: runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Build: Build,
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				identity.Revision = setting.Value
			case "vcs.modified":
				identity.Modified = setting.Value == "true"
			}
		}
	}
	return identity
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }() // Read-only: close cannot lose data.
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// readChecksums parses a sha256sum-format file into name -> digest.
func readChecksums(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	defer func() { _ = file.Close() }() // Read-only: close cannot lose data.
	sums := map[string]string{}
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		digest, name, ok := strings.Cut(text, " ")
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		if !ok || len(digest) != 64 || name == "" {
			return nil, fmt.Errorf("%s:%d: not a SHA-256 checksum line", path, line)
		}
		sums[filepath.Base(name)] = strings.ToLower(digest)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	return sums, nil
}

func verifyFile(path, kind string, sums map[string]string, opts verifyOptions) verifyTarget {
	target := verifyTarget{Path: path, Kind: kind}
	digest, err := fileSHA256(path)
	if err != nil {
		target.Checks = append(target.Checks, verifyCheck{Name: "digest", Status: "failed", Detail: err.Error()})
		return target
	}
	target.SHA256 = digest
	if sums != nil {
		switch want, ok := sums[filepath.Base(path)]; {
		case !ok:
			target.Checks = append(target.Checks, verifyCheck{Name: "checksum", Status: "failed", Detail: filepath.Base(path) + " is not listed in " + opts.checksums})
		case want != digest:
			target.Checks = append(target.Checks, verifyCheck{Name: "checksum", Status: "failed", Detail: "digest differs from " + opts.checksums + " (" + want + ")"})
		default:
			target.Checks = append(target.Checks, verifyCheck{Name: "checksum", Status: "ok", Detail: "matches " + opts.checksums})
		}
	}
	if opts.provenance {
		target.Checks = append(target.Checks, verifyProvenance(path, opts.repo, opts.signerWorkflow))
	}
	return target
}

// verifyProvenance checks the GitHub build-provenance attestation with the
// gh CLI, which verifies the Sigstore bundle: certificate chain, transparency
// log inclusion, and a signer that must be the repository's release workflow.
func verifyProvenance(path, repo, signerWorkflow string) verifyCheck {
	if signerWorkflow == "" {
		signerWorkflow = repo + "/.github/workflows/release.yml"
	}
	args := []string{"attestation", "verify", path, "--repo", repo,
		"--signer-workflow", signerWorkflow}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return verifyCheck{Name: "provenance", Status: "unavailable", Detail: "install the GitHub CLI (https://cli.github.com), then run: gh " + strings.Join(args, " ")}
	}
	output, err := exec.Command(gh, args...).CombinedOutput()
	if err != nil {
		return verifyCheck{Name: "provenance", Status: "failed", Detail: strings.TrimSpace(string(output))}
	}
	return verifyCheck{Name: "provenance", Status: "ok", Detail: "signed by " + signerWorkflow}
}

func verifyPath(path string, sums map[string]string, opts verifyOptions) verifyTarget {
	if _, err := os.Stat(path); err != nil {
		return verifyTarget{Path: path, Kind: "missing", Checks: []verifyCheck{{Name: "exists", Status: "failed", Detail: err.Error()}}}
	}
	return verifyFile(path, "file", sums, opts)
}

func writeVerifyText(stdout io.Writer, report verifyReport) {
	id := report.Identity
	_, _ = fmt.Fprintf(stdout, "wopr %s %s %s %s, build %s\n", id.Version, id.Go, id.Platform, id.Revision, id.Build)
	for _, target := range report.Targets {
		_, _ = fmt.Fprintf(stdout, "\n%s (%s)\n", target.Path, target.Kind)
		if target.SHA256 != "" {
			_, _ = fmt.Fprintf(stdout, "  sha256:%s\n", target.SHA256)
		}
		for _, check := range target.Checks {
			mark := map[string]string{"ok": "ok  ", "failed": "FAIL", "unavailable": "n/a "}[check.Status]
			_, _ = fmt.Fprintf(stdout, "  %s %s", mark, check.Name)
			if check.Detail != "" {
				_, _ = fmt.Fprintf(stdout, ": %s", strings.ReplaceAll(check.Detail, "\n", "\n       "))
			}
			_, _ = io.WriteString(stdout, "\n")
		}
	}
	if len(report.Targets) == 1 && report.Targets[0].Kind == "binary" && len(report.Targets[0].Checks) == 0 {
		_, _ = io.WriteString(stdout, "\nCompare the digest with the release SHA256SUMS (--checksums), and check provenance with --provenance.\n")
	}
}
