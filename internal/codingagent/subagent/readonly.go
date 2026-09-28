package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Read-only shell: explore subagents get bash restricted to commands that
// only read. A command is a pipeline of allowlisted programs; chaining,
// redirection, substitution, and flags that write or run other programs are
// rejected before anything executes.

var readOnlyPrograms = map[string]bool{
	"rg": true, "grep": true, "cat": true, "head": true, "tail": true, "wc": true,
	"ls": true, "find": true, "git": true, "go": true, "sed": true, "sort": true,
	"uniq": true, "cut": true, "tr": true, "nl": true, "file": true, "stat": true,
	"du": true, "tree": true, "pwd": true, "basename": true, "dirname": true,
	"realpath": true, "jq": true,
}

var gitReadOnly = []string{"log", "show", "diff", "blame", "status", "ls-files", "grep", "rev-parse", "describe", "shortlog", "cat-file", "ls-tree", "merge-base"}

var goReadOnly = []string{"doc", "list", "version", "env"}

var sedPrintRe = regexp.MustCompile(`^'?[0-9]+(,[0-9$]+)?p'?$`)

// CheckReadOnly reports why command is not a read-only pipeline, or nil.
func CheckReadOnly(command string) error {
	stages, err := pipelineWords(command)
	if err != nil {
		return err
	}
	for _, words := range stages {
		if len(words) == 0 {
			return errors.New("read-only shell: empty pipeline stage")
		}
		if err := checkProgram(words); err != nil {
			return err
		}
	}
	return nil
}

func checkProgram(words []string) error {
	program, args := words[0], words[1:]
	if !readOnlyPrograms[program] {
		return fmt.Errorf("read-only shell: %s is not allowed (allowed: rg, grep, git log/show/diff/blame/status/ls-files/grep, go doc/list, cat, head, tail, wc, ls, find, sed -n Np)", program)
	}
	deny := func(prefixes ...string) error {
		for _, arg := range args {
			for _, p := range prefixes {
				if arg == p || strings.HasPrefix(arg, p+"=") {
					return fmt.Errorf("read-only shell: %s %s is not allowed", program, p)
				}
			}
		}
		return nil
	}
	switch program {
	case "rg":
		return deny("--pre")
	case "find":
		return deny("-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls")
	case "git":
		if len(args) == 0 || !slices.Contains(gitReadOnly, args[0]) {
			return fmt.Errorf("read-only shell: git %s is not allowed", strings.Join(args[:min(1, len(args))], ""))
		}
		return deny("--output", "-O", "--open-files-in-pager", "--ext-diff", "--textconv")
	case "go":
		if len(args) == 0 || !slices.Contains(goReadOnly, args[0]) {
			return fmt.Errorf("read-only shell: go %s is not allowed", strings.Join(args[:min(1, len(args))], ""))
		}
		return deny("-w", "-u", "-exec", "-toolexec")
	case "sed":
		if len(args) < 2 || args[0] != "-n" || !sedPrintRe.MatchString(args[1]) {
			return errors.New("read-only shell: only sed -n 'N,Mp' is allowed")
		}
	case "sort":
		return deny("-o", "--output")
	}
	return nil
}

// pipelineWords splits a command into pipeline stages of words, honoring
// single and double quotes and backslash escapes. It rejects every shell
// operator other than a single "|", and substitution anywhere it would run.
func pipelineWords(s string) ([][]string, error) {
	var stages [][]string
	var words []string
	var b strings.Builder
	inWord, escaped := false, false
	var quote rune
	endWord := func() {
		if inWord {
			words = append(words, b.String())
			b.Reset()
			inWord = false
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '`' || (r == '$' && (next == '(' || next == '{')):
			return nil, errors.New("read-only shell: command substitution is not allowed")
		case r == '\\':
			escaped, inWord = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			endWord()
		case r == '|':
			if next == '|' {
				return nil, errors.New("read-only shell: \"||\" is not allowed")
			}
			endWord()
			stages = append(stages, words)
			words = nil
		case strings.ContainsRune(";&<>\n\r(){}", r):
			return nil, fmt.Errorf("read-only shell: %q is not allowed; run one read-only command or a pipeline", string(r))
		default:
			b.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("read-only shell: unterminated quote")
	}
	endWord()
	return append(stages, words), nil
}

// readOnlyShell wraps a bash tool so only read-only pipelines run.
type readOnlyShell struct{ bash agent.AgentTool }

// ReadOnlyShell restricts bash to CheckReadOnly commands.
func ReadOnlyShell(bash agent.AgentTool) agent.AgentTool { return readOnlyShell{bash: bash} }

func (t readOnlyShell) Name() string  { return t.bash.Name() }
func (t readOnlyShell) Label() string { return t.bash.Label() }

func (t readOnlyShell) Schema() ai.ToolSchema {
	schema := t.bash.Schema()
	schema.Description = "Run one read-only command or pipeline in the working directory: rg, grep, git log/show/diff/blame/status/ls-files/grep, go doc/list, cat, head, tail, wc, ls, find, sed -n 'N,Mp'. Chaining, redirection, substitution, and anything that writes are rejected."
	schema.PromptGuidelines = nil
	return schema
}

func (t readOnlyShell) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

func (t readOnlyShell) Execute(ctx context.Context, id string, params json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := CheckReadOnly(in.Command); err != nil {
		return agent.AgentToolResult{Content: err.Error(), IsError: true}, nil
	}
	return t.bash.Execute(ctx, id, params, onUpdate)
}
