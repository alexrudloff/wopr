package tools

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Bash output compaction: known-noisy commands lose the lines that carry no
// signal (git hints, passing test lines, repeated ownership columns) before
// the output reaches the model. Filters only drop lines they recognize, never
// reword the rest, and never drop a failure. A command whose output the model
// shaped itself (a pipe or a redirect) is left alone, so `| cat` is the raw
// escape hatch.

// compactNoteMinBytes is how much compaction must remove before the result
// says so and the raw output is archived.
const compactNoteMinBytes = 1024

// minCollapseRun is the shortest run of similar lines that collapses.
const minCollapseRun = 6

// maxGitLogCommits caps a default-format git log.
const maxGitLogCommits = 50

// dataCommands print file content or search results, whose similar lines
// are data rather than progress noise, so they never collapse.
var dataCommands = []string{"cat", "head", "tail", "sed", "awk", "grep", "egrep", "rg", "ag", "find", "fd", "ls", "tree", "git", "diff", "jq", "yq", "nl", "sort", "uniq", "cut", "xxd", "od", "hexdump", "strings", "column"}

// shellFilter compacts the output of one recognized command.
type shellFilter func(args []string, lines []string) []string

// CompactShellOutput returns output compacted for command, or output itself
// when nothing applies.
func CompactShellOutput(command, output string) string {
	args, ok := simpleCommand(command)
	if !ok || output == "" {
		return output
	}
	lines := strings.Split(output, "\n")
	if filter := filterFor(args); filter != nil {
		lines = filter(args, lines)
	}
	if !slices.Contains(dataCommands, args[0]) {
		lines = collapseSimilarRuns(lines)
	}
	compacted := strings.Join(lines, "\n")
	if len(compacted) >= len(output) {
		return output
	}
	return compacted
}

// simpleCommand returns the words of the command whose output the tool
// returns: the last of a && or ; chain, without leading environment
// assignments or time. A pipe, redirect, or substitution disqualifies the
// command; 2>&1 does not.
func simpleCommand(command string) ([]string, bool) {
	command = strings.ReplaceAll(command, "2>&1", " ")
	if strings.ContainsAny(command, "|<>`\n") || strings.Contains(command, "$(") {
		return nil, false
	}
	for _, sep := range []string{"&&", ";"} {
		if i := strings.LastIndex(command, sep); i >= 0 {
			command = command[i+len(sep):]
		}
	}
	args := strings.Fields(command)
	for len(args) > 0 && (strings.Contains(args[0], "=") || args[0] == "time" || args[0] == "command") {
		args = args[1:]
	}
	return args, len(args) > 0
}

func filterFor(args []string) shellFilter {
	switch args[0] {
	case "git":
		return gitFilter(args)
	case "go":
		if len(args) > 1 && args[1] == "test" && !slices.Contains(args, "-json") {
			return compactGoTest
		}
	case "cargo":
		if len(args) > 1 && (args[1] == "test" || args[1] == "nextest") {
			return dropMatching(cargoNoise)
		}
	case "ls":
		if slices.ContainsFunc(args[1:], func(a string) bool {
			return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "l")
		}) {
			return compactLongListing
		}
	}
	if slices.Contains(args, "pytest") {
		return dropMatching(pytestNoise)
	}
	switch args[0] {
	case "npm", "npx", "yarn", "pnpm", "bun", "jest", "vitest", "mocha":
		if slices.ContainsFunc(args, func(a string) bool { return a == "test" || a == "jest" || a == "vitest" || a == "mocha" }) {
			return dropMatching(jsTestNoise)
		}
	}
	return nil
}

// dropMatching drops the lines pattern matches.
func dropMatching(pattern *regexp.Regexp) shellFilter {
	return func(_ []string, lines []string) []string {
		return slices.DeleteFunc(lines, pattern.MatchString)
	}
}

var (
	cargoNoise  = regexp.MustCompile(`^(test \S+ \.\.\. ok|\s*Compiling \S+ v\S+.*)$`)
	pytestNoise = regexp.MustCompile(`^(\S+\.py [.sx]+\s*(\[\s*\d+%\])?|\S+::\S+ (PASSED|SKIPPED|XFAIL)\s*(\[\s*\d+%\])?)$`)
	jsTestNoise = regexp.MustCompile(`^\s*(✓|✔|√|PASS )\s*\S`)
)

// gitFilter picks the filter for a git subcommand, skipping git's global
// options.
func gitFilter(args []string) shellFilter {
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-C" || args[i] == "-c" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return nil
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "status":
		if slices.ContainsFunc(rest, func(a string) bool { return a == "-s" || a == "--short" || strings.HasPrefix(a, "--porcelain") }) {
			return nil
		}
		return compactGitStatus
	case "diff", "show":
		return dropDiffIndexLines
	case "log":
		if slices.ContainsFunc(rest, func(a string) bool {
			return strings.HasPrefix(a, "--format") || strings.HasPrefix(a, "--pretty") || a == "-n" || strings.HasPrefix(a, "--max-count") || gitLogCount.MatchString(a)
		}) {
			return nil
		}
		return capGitLog
	}
	return nil
}

var (
	gitHint      = regexp.MustCompile(`^\s*\((use "git |commit or discard ).*\)$`)
	gitHintTail  = regexp.MustCompile(` \(use "git [^)]*\)$`)
	gitLogCount  = regexp.MustCompile(`^-\d+$`)
	diffIndex    = regexp.MustCompile(`^index [0-9a-f]+\.\.[0-9a-f]+( \d+)?$`)
	goTestOK     = regexp.MustCompile(`^ok\s+\S+\s+(\(cached\)|[\d.]+s)`)
	goTestNoTest = regexp.MustCompile(`^\?\s+\S+\s+\[no test files\]$`)
	goTestFrame  = regexp.MustCompile(`^\s*(=== (RUN|PAUSE|CONT|NAME)\s|--- PASS: |PASS$)`)
)

func compactGitStatus(_ []string, lines []string) []string {
	out := lines[:0]
	for _, line := range lines {
		if gitHint.MatchString(line) {
			continue
		}
		out = append(out, gitHintTail.ReplaceAllString(line, ""))
	}
	return out
}

// dropDiffIndexLines drops the blob-hash line under each diff header.
func dropDiffIndexLines(_ []string, lines []string) []string {
	var out []string
	for i, line := range lines {
		if i > 0 && strings.HasPrefix(lines[i-1], "diff --git ") && diffIndex.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func capGitLog(_ []string, lines []string) []string {
	commits := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, "commit ") {
			continue
		}
		if commits++; commits > maxGitLogCommits {
			rest := 0
			for _, l := range lines[i:] {
				if strings.HasPrefix(l, "commit ") {
					rest++
				}
			}
			return append(lines[:i:i], fmt.Sprintf("… %d more commits (git log --skip=%d for more)", rest, maxGitLogCommits))
		}
	}
	return lines
}

// compactGoTest drops passing-package lines and test framing, keeps every
// failure and the output of failing tests, and summarizes what it dropped.
func compactGoTest(_ []string, lines []string) []string {
	passed, noTests := 0, 0
	var out []string
	for _, line := range lines {
		switch {
		case goTestOK.MatchString(line):
			passed++
		case goTestNoTest.MatchString(line):
			noTests++
		case goTestFrame.MatchString(line):
		default:
			out = append(out, line)
		}
	}
	if passed+noTests == 0 {
		return out
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return append(out, fmt.Sprintf("go test: %d packages ok, %d without tests", passed, noTests))
}

var longListing = regexp.MustCompile(`^([-dlcbps][-rwxsStT@+.]{9,11})\s+(\d+)\s+(\S+)\s+(\S+)\s+(.*)$`)

// compactLongListing drops the total line, the . and .. entries, and the
// link count, owner, and group columns when every entry shares one owner
// and group.
func compactLongListing(_ []string, lines []string) []string {
	owner, uniform := "", true
	for _, line := range lines {
		if m := longListing.FindStringSubmatch(line); m != nil {
			if owner == "" {
				owner = m[3] + " " + m[4]
			} else if owner != m[3]+" "+m[4] {
				uniform = false
			}
		}
	}
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "total ") {
			continue
		}
		m := longListing.FindStringSubmatch(line)
		if m == nil {
			out = append(out, line)
			continue
		}
		if strings.HasSuffix(m[5], " .") || strings.HasSuffix(m[5], " ..") {
			continue
		}
		if uniform {
			line = m[1] + " " + m[5]
		}
		out = append(out, line)
	}
	if uniform && owner != "" {
		out = append([]string{"(owner " + owner + ")"}, out...)
	}
	return out
}

var (
	digits      = regexp.MustCompile(`\d+`)
	failureWord = regexp.MustCompile(`(?i)fail|error|panic|fatal|exception|traceback|warn`)
)

// collapseSimilarRuns replaces the middle of a run of lines that differ
// only in their numbers (progress lines, repeated log lines) with a count.
// Lines that mention a failure never collapse.
func collapseSimilarRuns(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); {
		key := digits.ReplaceAllString(lines[i], "0")
		j := i + 1
		if strings.TrimSpace(lines[i]) != "" && !failureWord.MatchString(lines[i]) {
			for j < len(lines) && digits.ReplaceAllString(lines[j], "0") == key {
				j++
			}
		}
		if j-i < minCollapseRun {
			out = append(out, lines[i:j]...)
		} else {
			out = append(out, lines[i], lines[i+1], fmt.Sprintf("… %d similar lines", j-i-3), lines[j-1])
		}
		i = j
	}
	return out
}
