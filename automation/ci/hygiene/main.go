// Command hygiene rejects private coordination paths and infrastructure
// references in tracked source.
//
//	go run ./automation/ci/hygiene             tracked working files (source hygiene)
//	go run ./automation/ci/hygiene -ref REF    the immutable Git tree at REF (publication)
//
// It supplements, and does not replace, secret scanning and review of
// third-party redistribution rights.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/automation/internal/textlines"
)

// Operator-specific literals are assembled so this policy does not flag itself.
var (
	private = regexp.MustCompile(
		`(?:/Users/|/home/)` + "kin" + `sy(?:/|\b)|` +
			"imla" + `dris|(?:[\w.-]+\.)?hpe` + `corp\.net|labs\.hpe` + `corp|` +
			"wopr" + `-staging|(?:~/|\$HOME/)wopr` + `-lanes|` +
			`\.dev` + `cache/scratch|(?:/Users|/home)/[^/\s]+/WOPR-launch`)
	lane       = regexp.MustCompile(`(^|/)(REVIEW[^/]*|QUESTIONS|REPORT|NATIVE-PROOF-REQUEST[^/]*|[^/]*-REPORT|TASK|[^/]*\.TASK)\.md$`)
	envFile    = regexp.MustCompile(`(^|/)(\.env(\.[^/]*)?|[^/]+\.env)$`)
	privateDir = regexp.MustCompile(`(^|/)(\.dev` + `cache|wopr-handoff)/`)
)

func main() {
	os.Exit(run(".", os.Args[1:], os.Stdout, os.Stderr))
}

func run(dir string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hygiene", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ref := flags.String("ref", "", "scan committed blobs at this Git ref, not working files")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	failures, err := scan(dir, *ref)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hygiene:", err)
		return 1
	}
	if len(failures) > 0 {
		_, _ = fmt.Fprintln(stderr, strings.Join(failures, "\n"))
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "public hygiene: tracked paths and private-reference checks passed")
	return 0
}

// findings reports a forbidden path and every line holding a private reference.
// Line contents are never echoed: they may be sensitive and land in CI logs.
func findings(path string, data []byte) []string {
	var out []string
	if lane.MatchString(path) || privateDir.MatchString(path) || envFile.MatchString(path) {
		out = append(out, path+": forbidden publication path")
	}
	// Match binary content too: an embedded private path is still private.
	for number, line := range textlines.Split(data) {
		if private.Match(line) {
			out = append(out, fmt.Sprintf("%s:%d: private infrastructure or operator path", path, number+1))
		}
	}
	return out
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func scan(dir, ref string) ([]string, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(string(top))
	if ref != "" {
		return scanTree(root, ref)
	}
	names, err := git(root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var failures []string
	for name := range strings.SplitSeq(string(names), "\x00") {
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if errors.Is(err, fs.ErrNotExist) { // a tracked deletion in the candidate
			continue
		}
		if err != nil {
			return nil, err
		}
		failures = append(failures, findings(name, data)...)
	}
	return failures, nil
}

// scanTree reads every blob of ref's tree through one `git cat-file --batch`
// process, not a Git subprocess per file of a release tree.
func scanTree(root, ref string) ([]string, error) {
	tree, err := git(root, "rev-parse", ref+"^{tree}")
	if err != nil {
		return nil, err
	}
	entries, err := git(root, "ls-tree", "-r", "-z", strings.TrimSpace(string(tree)))
	if err != nil {
		return nil, err
	}
	batch := exec.Command("git", "-C", root, "cat-file", "--batch")
	stdin, err := batch.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := batch.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := batch.Start(); err != nil {
		return nil, err
	}
	waited := false
	defer func() {
		if !waited {
			_ = batch.Process.Kill()
			_ = batch.Wait()
		}
	}()
	reader := bufio.NewReader(stdoutPipe)
	var failures []string
	for entry := range bytes.SplitSeq(entries, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		metadata, path, ok := bytes.Cut(entry, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected tree entry %q", entry)
		}
		kind, oid := string(fields[1]), string(fields[2])
		if kind != "blob" {
			return nil, errors.New("non-blob tree entry requires explicit publication review")
		}
		if _, err := io.WriteString(stdin, oid+"\n"); err != nil {
			return nil, err
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, errors.New("could not read committed blob")
		}
		parts := strings.Fields(header)
		if len(parts) != 3 || parts[0] != oid || parts[1] != "blob" {
			return nil, errors.New("could not read committed blob")
		}
		size, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, errors.New("could not read committed blob")
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, errors.New("truncated committed blob")
		}
		if b, err := reader.ReadByte(); err != nil || b != '\n' {
			return nil, errors.New("truncated committed blob")
		}
		failures = append(failures, findings(string(path), data)...)
	}
	if err := stdin.Close(); err != nil {
		return nil, err
	}
	waited = true
	if err := batch.Wait(); err != nil {
		return nil, errors.New("git blob reader failed")
	}
	return failures, nil
}
