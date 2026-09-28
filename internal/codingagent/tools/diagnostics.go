package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Post-edit diagnostics: after edit or write succeeds, a fast checker for
// the file's language runs with a short timeout and the result gains the
// problems that are new for that file, so a model sees a broken build in the
// same turn instead of discovering it several calls later. A missing checker,
// a timeout, or a checker failure adds nothing.

// DefaultDiagnosticsTimeout bounds one checker run.
const DefaultDiagnosticsTimeout = 4 * time.Second

// maxDiagnosticLines caps the problems one result lists.
const maxDiagnosticLines = 10

// DiagnosticsSettingsView is implemented by settings that configure
// post-edit diagnostics. commands maps a language (go, typescript,
// javascript, python, shell) to a command line with {file} and {dir}
// placeholders; "off" turns that language off.
type DiagnosticsSettingsView interface {
	GetDiagnostics() (enabled bool, timeout time.Duration, commands map[string]string)
}

// checker is one diagnostics command for a language.
type checker struct {
	name string
	// argv builds the command; nil means the checker does not apply.
	argv func(file string) []string
	// packageScope checkers report other files too; only lines naming the
	// edited file count. A file-scoped checker's whole output counts.
	packageScope bool
}

// Diagnostics runs post-edit checkers. The zero value is disabled.
type Diagnostics struct {
	Enabled  bool
	Timeout  time.Duration
	Commands map[string]string
	// LookPath resolves checker binaries; nil uses exec.LookPath.
	LookPath func(string) (string, error)

	mu       sync.Mutex
	found    map[string]bool     // binary → available
	baseline map[string][]string // file → problem keys of the last check
}

// NewDiagnostics configures diagnostics from settings; it is disabled
// unless settings provide a diagnostics configuration that is enabled.
func NewDiagnostics(settings any) *Diagnostics {
	view, ok := settings.(DiagnosticsSettingsView)
	if !ok {
		return nil
	}
	enabled, timeout, commands := view.GetDiagnostics()
	if !enabled {
		return nil
	}
	return &Diagnostics{Enabled: true, Timeout: timeout, Commands: commands}
}

// have reports whether bin is on PATH, looking it up once.
func (d *Diagnostics) have(bin string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ok, cached := d.found[bin]; cached {
		return ok
	}
	lookPath := d.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(bin)
	if d.found == nil {
		d.found = map[string]bool{}
	}
	d.found[bin] = err == nil
	return err == nil
}

// extensionLanguages maps a file extension to a diagnostics language.
var extensionLanguages = map[string]string{
	".go": "go",
	".ts": "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".py": "python",
	".sh": "shell", ".bash": "shell",
}

// languageOf maps a file extension to a diagnostics language.
func languageOf(path string) string {
	return extensionLanguages[strings.ToLower(filepath.Ext(path))]
}

// pythonSyntaxCheck parses a file without writing bytecode next to it.
const pythonSyntaxCheck = "import ast,sys\ntry: ast.parse(open(sys.argv[1],'rb').read(),sys.argv[1])\nexcept SyntaxError as e: print(f'{e.filename}:{e.lineno}:{e.offset}: SyntaxError: {e.msg}'); sys.exit(1)"

// checkers returns the candidate checkers for a language, best first.
func (d *Diagnostics) checkers(lang, file string) []checker {
	if command, ok := d.Commands[lang]; ok {
		if strings.TrimSpace(command) == "off" || strings.TrimSpace(command) == "" {
			return nil
		}
		fields := strings.Fields(command)
		return []checker{{name: fields[0], argv: func(file string) []string {
			out := make([]string, len(fields))
			for i, f := range fields {
				f = strings.ReplaceAll(f, "{file}", file)
				out[i] = strings.ReplaceAll(f, "{dir}", filepath.Dir(file))
			}
			return out
		}}}
	}
	switch lang {
	case "go":
		fallback := checker{name: "go build", packageScope: true, argv: func(string) []string { return []string{"go", "build", "-o", os.DevNull, "."} }}
		if strings.HasSuffix(file, "_test.go") {
			// go build skips test files; vet compiles them.
			fallback = checker{name: "go vet", packageScope: true, argv: func(string) []string { return []string{"go", "vet", "."} }}
		}
		return []checker{
			{name: "gopls", packageScope: true, argv: func(file string) []string { return []string{"gopls", "check", file} }},
			fallback,
		}
	case "typescript":
		return []checker{{name: "tsc", packageScope: true, argv: projectTSC}}
	case "javascript":
		return []checker{{name: "node --check", argv: func(file string) []string { return []string{"node", "--check", file} }}}
	case "python":
		return []checker{
			{name: "ruff", argv: func(file string) []string {
				return []string{"ruff", "check", "--output-format=concise", "--no-fix", "--quiet", file}
			}},
			{name: "python", argv: func(file string) []string { return []string{"python3", "-c", pythonSyntaxCheck, file} }},
		}
	case "shell":
		return []checker{{name: "bash -n", argv: func(file string) []string { return []string{"bash", "-n", file} }}}
	}
	return nil
}

// projectTSC runs the project's own tsc against the nearest tsconfig.json;
// a project without both has no TypeScript checker.
func projectTSC(file string) []string {
	var tsconfig, tsc string
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if tsconfig == "" {
			if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err == nil {
				tsconfig = dir
			}
		}
		if tsconfig != "" {
			candidate := filepath.Join(dir, "node_modules", ".bin", "tsc")
			if _, err := os.Stat(candidate); err == nil {
				tsc = candidate
				break
			}
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	if tsc == "" {
		return nil
	}
	return []string{tsc, "--noEmit", "--pretty", "false", "-p", tsconfig}
}

// Check runs the first available checker for absPath and returns the new
// problems for that file as a short block, or "" when there are none.
// displayPath is how the model named the file.
func (d *Diagnostics) Check(ctx context.Context, absPath, displayPath string) string {
	if d == nil || !d.Enabled {
		return ""
	}
	lang := languageOf(absPath)
	if lang == "" {
		return ""
	}
	for _, c := range d.checkers(lang, absPath) {
		argv := c.argv(absPath)
		if len(argv) == 0 || (!filepath.IsAbs(argv[0]) && !d.have(argv[0])) {
			continue
		}
		problems, ok := d.run(ctx, c, argv, absPath, displayPath)
		if !ok {
			return ""
		}
		return d.report(c.name, absPath, problems)
	}
	return ""
}

// run executes one checker; ok is false when it timed out or could not
// start, so nothing is reported.
func (d *Diagnostics) run(ctx context.Context, c checker, argv []string, absPath, displayPath string) ([]string, bool) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultDiagnosticsTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = filepath.Dir(absPath)
	cmd.WaitDelay = 500 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, false
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return nil, false
	}
	base := filepath.Base(absPath)
	mention := regexp.MustCompile(`\S*` + regexp.QuoteMeta(base) + `\b`)
	var problems []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		line = strings.TrimRight(line, " \r\t")
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		named := strings.Contains(line, base)
		if c.packageScope && !named {
			continue
		}
		if named {
			line = mention.ReplaceAllLiteralString(line, displayPath)
		}
		problems = append(problems, strings.TrimPrefix(line, "vet: "))
	}
	if err == nil && !c.packageScope {
		// A clean exit from a file-scoped checker is a clean file, whatever
		// it printed.
		problems = nil
	}
	return problems, true
}

var problemPosition = regexp.MustCompile(`:\d+(?:[.:-]\d+)*|\bline \d+`)

// report keeps the problems that were not in the file's last check and
// formats them, then records this check as the new baseline. Positions are
// ignored when comparing, so an edit that shifts lines does not make old
// problems look new.
func (d *Diagnostics) report(name, absPath string, problems []string) string {
	keys := make([]string, len(problems))
	for i, p := range problems {
		keys[i] = problemPosition.ReplaceAllString(p, ":")
	}
	d.mu.Lock()
	previous := d.baseline[absPath]
	if d.baseline == nil {
		d.baseline = map[string][]string{}
	}
	d.baseline[absPath] = keys
	d.mu.Unlock()
	seen := map[string]int{}
	for _, k := range previous {
		seen[k]++
	}
	var fresh []string
	for i, p := range problems {
		if seen[keys[i]] > 0 {
			seen[keys[i]]--
			continue
		}
		fresh = append(fresh, p)
	}
	if len(fresh) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "New problems (%s):", name)
	for i, p := range fresh {
		if i == maxDiagnosticLines {
			fmt.Fprintf(&b, "\n… %d more", len(fresh)-i)
			break
		}
		b.WriteString("\n")
		b.WriteString(p)
	}
	return b.String()
}
