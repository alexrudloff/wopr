// Package subagent runs delegated tasks: a brief goes to a child agent with
// fresh context, read-only tools, and budgets; its answer comes back in a
// fixed format whose quotes wopr verifies before the orchestrator sees them.
// The contract follows the Evidence-Preserving Reducer: delegation never
// requires trusting a fluent summary.
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Result statuses a child may report.
const (
	StatusDone    = "done"
	StatusPartial = "partial"
	StatusFailed  = "failed"
	StatusBlocked = "blocked"
)

// MaxEvidence is how many quotes a result keeps, as the reducer's receipts.
const MaxEvidence = 12

// Result is a child's parsed final answer.
type Result struct {
	Status     string
	Confidence string
	Answer     string
	Evidence   []Evidence
	NotChecked string
	// Parsed reports that the text carried a STATUS line.
	Parsed bool
	// Dropped counts evidence lines beyond MaxEvidence.
	Dropped int
}

// Evidence is one quoted line and whether wopr could verify it.
type Evidence struct {
	Raw     string
	Command string // set for "cmd:" evidence
	Path    string // set for file evidence
	Start   int
	End     int
	Quote   string
	// splits are the other readings of a command line whose command itself
	// contains double quotes: each command/quote split at a ` "`.
	splits [][2]string
	// Verified is set by Verify; Line is where a file quote was found.
	Verified bool
	Line     int
	Problem  string
}

var headerRe = regexp.MustCompile(`(?i)^[#*\s_]*(STATUS|CONFIDENCE|ANSWER|EVIDENCE|NOT[_ ]CHECKED|CHANGED)[*_\s]*:[*_]*\s?(.*)$`)

// ParseResult reads the final message format, tolerating prose and
// markdown around it. Text without a STATUS line becomes a partial answer.
func ParseResult(text string) Result {
	var r Result
	sections := map[string]*strings.Builder{}
	current := ""
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if m := headerRe.FindStringSubmatch(line); m != nil {
			current = strings.ReplaceAll(strings.ToUpper(m[1]), " ", "_")
			sections[current] = &strings.Builder{}
			sections[current].WriteString(m[2])
			continue
		}
		if b := sections[current]; b != nil {
			b.WriteString("\n" + line)
		}
	}
	get := func(key string) string {
		if b := sections[key]; b != nil {
			return strings.TrimSpace(b.String())
		}
		return ""
	}
	status := strings.ToLower(firstWord(get("STATUS")))
	switch status {
	case StatusDone, StatusPartial, StatusFailed, StatusBlocked:
		r.Status, r.Parsed = status, true
	default:
		r.Status = StatusPartial
	}
	r.Confidence = strings.ToLower(firstWord(get("CONFIDENCE")))
	switch r.Confidence {
	case "high", "medium", "low":
	default:
		r.Confidence = "low"
	}
	r.Answer = get("ANSWER")
	if !r.Parsed && r.Answer == "" {
		r.Answer = strings.TrimSpace(text)
	}
	r.NotChecked = get("NOT_CHECKED")
	for line := range strings.SplitSeq(get("EVIDENCE"), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•"))
		if line == "" || strings.EqualFold(line, "none") {
			continue
		}
		if len(r.Evidence) == MaxEvidence {
			r.Dropped++
			continue
		}
		r.Evidence = append(r.Evidence, parseEvidence(line))
	}
	return r
}

func firstWord(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "*_`|")
	if i := strings.IndexAny(s, " \t|,.;"); i > 0 {
		s = s[:i]
	}
	return s
}

var locationRe = regexp.MustCompile(`^(.+?):(\d+)(?:\s*[-–]\s*(\d+))?$`)

func parseEvidence(line string) Evidence {
	e := Evidence{Raw: line}
	head, quote, ok := splitQuote(line)
	if ok {
		e.Quote = quote
	}
	head = strings.TrimSpace(head)
	if rest, isCmd := strings.CutPrefix(head, "cmd:"); isCmd {
		e.Command = strings.Trim(strings.TrimSpace(rest), "`")
		e.splits = commandSplits(line)
		return e
	}
	head = strings.Trim(head, "`")
	if m := locationRe.FindStringSubmatch(head); m != nil {
		e.Path = m[1]
		e.Start, _ = strconv.Atoi(m[2])
		e.End = e.Start
		if m[3] != "" {
			e.End, _ = strconv.Atoi(m[3])
		}
	} else if head != "" {
		e.Path = strings.Fields(head)[0]
	}
	return e
}

// commandSplits reads a "cmd:" line whose command contains double quotes,
// as in cmd: rg -n "X" "output line": the quote may open at any ` "` after
// the first, closing at the line's last double quote. Later openings give
// shorter quotes, so they are listed first.
func commandSplits(line string) [][2]string {
	last := strings.LastIndex(line, `"`)
	first := strings.Index(line, `"`)
	var splits [][2]string
	for i := last - 1; i > first; i-- {
		if line[i] != '"' || line[i-1] != ' ' {
			continue
		}
		head, _ := strings.CutPrefix(strings.TrimSpace(line[:i]), "cmd:")
		quote := line[i+1 : last]
		if len(strings.TrimSpace(quote)) >= minSplitQuote {
			splits = append(splits, [2]string{strings.Trim(strings.TrimSpace(head), "`"), quote})
		}
	}
	return splits
}

// minSplitQuote keeps a re-split command quote from shrinking to a
// fragment that matches anything.
const minSplitQuote = 8

// splitQuote returns the text before the quote and the quote itself: the
// span between the first and last double quote, else between backticks.
func splitQuote(line string) (head, quote string, ok bool) {
	for _, q := range []string{`"`, "`"} {
		first := strings.Index(line, q)
		last := strings.LastIndex(line, q)
		if first >= 0 && last > first {
			return line[:first], line[first+1 : last], true
		}
	}
	return line, "", false
}

const (
	// lineSlack is how far a file quote may sit from its cited range.
	lineSlack = 50
	// maxVerifyBytes bounds the files read for verification.
	maxVerifyBytes = 8 << 20
)

// Verify checks every quote: a file quote must be a byte-exact substring of
// the file near its cited lines; a command quote must be a byte-exact
// substring of one of the child's tool outputs. It returns the counts.
func (r *Result) Verify(cwd string, outputs []string) (ok, total int) {
	files := map[string]string{}
	for i := range r.Evidence {
		e := &r.Evidence[i]
		verifyOne(e, cwd, outputs, files)
		total++
		if e.Verified {
			ok++
		}
	}
	return ok, total
}

func verifyOne(e *Evidence, cwd string, outputs []string, files map[string]string) {
	quotes := quoteForms(e.Quote)
	if len(quotes) == 0 {
		e.Problem = "no quote"
		return
	}
	if e.Command != "" || e.Path == "" {
		if inOutputs(quotes, outputs) {
			e.Verified = true
			return
		}
		for _, s := range slices.Backward(e.splits) {
			if inOutputs(quoteForms(s[1]), outputs) {
				e.Command, e.Quote, e.Verified = s[0], s[1], true
				return
			}
		}
		e.Problem = "not in any tool output"
		return
	}
	path := e.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	body, seen := files[path]
	if !seen {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() <= maxVerifyBytes {
			if data, err := os.ReadFile(path); err == nil {
				body = string(data)
			}
		}
		files[path] = body
	}
	if body == "" {
		e.Problem = "file not readable"
		return
	}
	for _, q := range quotes {
		for offset := 0; ; {
			i := strings.Index(body[offset:], q)
			if i < 0 {
				break
			}
			at := offset + i
			line := strings.Count(body[:at], "\n") + 1
			if e.Start == 0 || (line >= e.Start-lineSlack && line <= max(e.End, e.Start)+lineSlack) {
				e.Verified, e.Line = true, line
				return
			}
			offset = at + 1
		}
	}
	e.Problem = "not found in file"
	if e.Start > 0 {
		e.Problem = fmt.Sprintf("not found near line %d", e.Start)
	}
}

// quoteForms is the quote as given and with common escapes undone.
func inOutputs(quotes, outputs []string) bool {
	for _, out := range outputs {
		for _, q := range quotes {
			if strings.Contains(out, q) {
				return true
			}
		}
	}
	return false
}

func quoteForms(q string) []string {
	if strings.TrimSpace(q) == "" {
		return nil
	}
	out := []string{q}
	if unescaped := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\t`, "\t").Replace(q); unescaped != q {
		out = append(out, unescaped)
	}
	return out
}
