// Package woprdocs materializes the documentation embedded in wopr under the
// user's config root (`~/.wopr/docs` by default). The pages come from
// docs/site/docs, the single documentation source.
//
// wopr ships as a standalone binary and cannot assume that a source checkout
// exists. The embedded bundle lets the running agent inspect the exact behavior
// its binary implements.
//
// The docs sync is idempotent and content-addressed. Users can also force a sync
// or read the bundle through `wopr docs`.
//
// Output layout
//
//	~/.wopr/docs/
//	  index.md             index and task router
//	  cli.md               CLI verbs and options
//	  slash-commands.md    slash commands
//	  configuration.md     paths and context files
//	  ...                  every other page under docs/site/docs
package woprdocs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	sitedocs "github.com/alexrudloff/wopr/docs/site/docs"
)

var content fs.FS = sitedocs.FS

const (
	// SubDir is the docs directory name under the wopr config root.
	SubDir = "docs"
	// markerFile records the digest of the embedded bundle that owns the
	// materialized directory.
	markerFile = ".wopr-docs-digest"
)

// DocsDir returns the absolute docs directory for a given config root.
func DocsDir(configRoot string) string {
	return filepath.Join(configRoot, SubDir)
}

// List returns the embedded doc filenames in stable alphabetical
// order. It is the canonical source for `wopr docs list` and for any
// caller that needs to enumerate available topics.
func List() []string {
	names, _ := fs.Glob(content, "*.md") // the pattern is valid, so Glob cannot fail
	return names
}

func contentDigest() (string, error) {
	hash := sha256.New()
	for _, name := range List() {
		data, err := fs.ReadFile(content, name)
		if err != nil {
			return "", fmt.Errorf("wopr docs: read %s for digest: %w", name, err)
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", name, len(data))
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Read returns the embedded bytes of a single doc file. Names may be
// given with or without the .md suffix; ".." and absolute paths are
// rejected so this is safe to drive from CLI input.
func Read(name string) ([]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("wopr docs: empty doc name")
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("wopr docs: invalid doc name %q", name)
	}
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	return fs.ReadFile(content, name)
}

// Sync writes every embedded doc into <configRoot>/docs, overwriting
// existing files. It also writes the content digest marker. Returns the
// list of relative paths written.
func Sync(configRoot string) ([]string, error) {
	digest, err := contentDigest()
	if err != nil {
		return nil, err
	}
	dir := DocsDir(configRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wopr docs: mkdir %s: %w", dir, err)
	}
	written := List()
	for _, name := range written {
		data, err := fs.ReadFile(content, name)
		if err != nil {
			return nil, fmt.Errorf("wopr docs: read %s: %w", name, err)
		}
		if err := writeFileAtomic(filepath.Join(dir, name), data, 0o644); err != nil {
			return nil, err
		}
	}
	// This directory is fully managed by wopr. Remove Markdown pages that were
	// present in an older bundle so renamed/folded topics cannot survive a sync
	// and mislead users or coding agents.
	onDisk, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("wopr docs: read managed dir: %w", err)
	}
	for _, entry := range onDisk {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if slices.Contains(written, entry.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return nil, fmt.Errorf("wopr docs: prune %s: %w", entry.Name(), err)
		}
	}
	if err := writeFileAtomic(filepath.Join(dir, markerFile), []byte(digest+"\n"), 0o644); err != nil {
		return nil, err
	}
	return written, nil
}

// EnsureSynced is the idempotent startup helper. It writes embedded docs only
// when the on-disk digest differs, so warm starts do no writable filesystem
// work. All errors are returned to the caller, which should treat any
// failure as non-fatal (logs a warning at most).
func EnsureSynced(configRoot string) error {
	digest, err := contentDigest()
	if err != nil {
		return err
	}
	dir := DocsDir(configRoot)
	marker := filepath.Join(dir, markerFile)
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == digest {
		return nil
	}
	_, err = Sync(configRoot)
	return err
}

// writeFileAtomic writes through a temp file + rename so a partial
// write never leaves a half-populated doc on disk.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".woprdocs-*")
	if err != nil {
		return fmt.Errorf("wopr docs: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wopr docs: write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wopr docs: chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("wopr docs: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("wopr docs: rename %s: %w", path, err)
	}
	return nil
}

// RunCommand implements the stock pre-session `wopr docs` command. It returns
// -1 when the arguments target another command.
//
//	wopr docs                       sync + summary (default)
//	wopr docs sync                  force re-sync embedded docs
//	wopr docs path                  print docs dir
//	wopr docs list                  list available doc files
//	wopr docs show <name>           print one doc to stdout
func RunCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "docs" {
		return -1
	}
	sub := ""
	rest := args[1:]
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "wopr docs: %v\n", err)
		return 1
	}
	root, err := configRoot()
	if err != nil {
		return fail(err)
	}
	switch sub {
	case "", "sync":
		written, err := Sync(root)
		if err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "wopr docs: synced %d files to %s\n", len(written), DocsDir(root))
		for _, name := range written {
			_, _ = fmt.Fprintf(stdout, "  %s\n", name)
		}
		return 0
	case "path":
		_, _ = fmt.Fprintln(stdout, DocsDir(root))
		return 0
	case "list":
		for _, name := range List() {
			_, _ = fmt.Fprintln(stdout, name)
		}
		return 0
	case "show":
		if len(rest) == 0 {
			_, _ = fmt.Fprintln(stderr, "wopr docs show <name>")
			return 2
		}
		data, err := Read(rest[0])
		if err != nil {
			return fail(err)
		}
		_, _ = stdout.Write(data)
		return 0
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "wopr docs: unknown subcommand %q\n", sub)
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: wopr docs [sync|path|list|show <name>]")
	_, _ = fmt.Fprintln(w, "  sync     write embedded docs into ~/.wopr/docs (default)")
	_, _ = fmt.Fprintln(w, "  path     print absolute docs directory")
	_, _ = fmt.Fprintln(w, "  list     list available doc filenames")
	_, _ = fmt.Fprintln(w, "  show     print one doc to stdout")
}

// configRoot mirrors codingagent.ConfigRoot to avoid an import cycle. The docs
// command runs before runtime services are constructed.
func configRoot() (string, error) {
	if v := os.Getenv("WOPR_HOME"); v != "" {
		return expandTilde(v), nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(expandTilde(v), "wopr"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wopr"), nil
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
