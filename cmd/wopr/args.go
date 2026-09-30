package main

import (
	_ "embed"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ─── CLI Flags ────────────────────────────────────────────────────────────────

// CLIFlags holds parsed command-line arguments.
type CLIFlags struct {
	Model   string
	Print   string
	Skills  []string
	Version bool
	Help    bool
	// Session selection flags.
	ResumeAny bool // --resume always opens the startup picker; use --session for path or ID
	Continue  bool // --continue: load most recent session non-interactively
	// Remaining args (the prompt in interactive mode)
	Args []string
	// Mode selects the output mode: text (default), json, or rpc.
	Mode string
	// Verbose forces verbose startup output (overrides quietStartup setting).
	Verbose bool

	// Provider overrides the default provider name (e.g. "openai", "anthropic").
	Provider string
	// SystemPrompt overrides the assembled system prompt.
	SystemPrompt string
	// AppendSystemPrompt appends text to the system prompt (repeatable).
	AppendSystemPrompt []string
	// Thinking sets the thinking level: off, minimal, low, medium, high, xhigh, max.
	Thinking string
	// NoSession disables session persistence (ephemeral).
	NoSession bool
	// Session specifies a session file path or partial UUID.
	Session string
	// Fork forks a specific session into a new session.
	Fork string
	// SessionDir overrides the session storage directory.
	SessionDir string
	// SessionID specifies an exact session ID to use or create.
	SessionID string
	// Name sets the session display name.
	Name string
	// NoTools disables all tools.
	NoTools bool
	// NoBuiltinTools disables built-in tools while leaving custom tools available.
	NoBuiltinTools bool
	// Tools is a comma-separated allowlist of tool names.
	Tools []string
	// ExcludeTools is a comma-separated list of tool names to exclude.
	ExcludeTools []string
	// ListModels prints available models and exits. Optional search pattern.
	ListModels    string
	ListModelsAll bool // bare --list-models (no pattern)
	// NoSkills disables skill discovery.
	NoSkills bool
	// PromptTemplates lists prompt template paths (repeatable).
	PromptTemplates []string
	// NoPromptTemplates disables prompt template discovery.
	NoPromptTemplates bool
	// Themes lists theme paths (repeatable).
	Themes []string
	// NoThemes disables theme discovery.
	NoThemes bool
	// NoContextFiles disables AGENTS.md/CLAUDE.md discovery.
	NoContextFiles bool
	// Export exports a session file to HTML and exits.
	Export string
	// APIKey overrides the API key.
	APIKey string
	// Offline disables startup network operations.
	Offline bool
	// GTW runs a print or JSON session in Global Thermonuclear War (--gtw or
	// WOPR_GTW=1).
	GTW bool
	// ProjectTrustOverride controls project-local resource loading for this run.
	// nil uses saved/default trust; true is --approve; false is --no-approve.
	ProjectTrustOverride *bool
	// FileArgs holds @file arguments (files included in prompt).
	FileArgs []string
	// UnknownOption is the first unrecognized option, also reported in
	// Diagnostics.
	UnknownOption string
	// UseTheme overrides the theme for this run.
	UseTheme string
	// Diagnostics collects parse warnings and errors. Any error exits before
	// version or mode handling.
	Diagnostics []argDiagnostic
}

// validThinkingLevels lists the accepted --thinking values.
var validThinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true,
	"medium": true, "high": true, "xhigh": true, "max": true,
}

// argDiagnostic is one argument-parsing diagnostic. Type is "warning" or "error".
type argDiagnostic struct {
	Type    string
	Message string
}

// reportArgDiagnostics writes diagnostics as red "Error: " and
// yellow "Warning: " lines on stderr, colored only for a terminal. It reports
// whether any diagnostic is an error, which exits with status 1.
func reportArgDiagnostics(w io.Writer, diagnostics []argDiagnostic, color bool) bool {
	hasError := false
	for _, diagnostic := range diagnostics {
		label, sgr := "Warning: ", "\x1b[33m"
		if diagnostic.Type == "error" {
			label, sgr = "Error: ", "\x1b[31m"
			hasError = true
		}
		text := label + diagnostic.Message
		if color {
			text = sgr + text + "\x1b[39m"
		}
		_, _ = fmt.Fprintln(w, text) // Best effort: stderr has no fallback channel.
	}
	return hasError
}

// parseFlags parses os.Args[1:] into CLIFlags.
func parseFlags(args []string) CLIFlags {
	flags := CLIFlags{Mode: "text"}
	boolFlags := map[string]*bool{
		"--version": &flags.Version, "-v": &flags.Version,
		"--help": &flags.Help, "-h": &flags.Help,
		"--continue": &flags.Continue, "-c": &flags.Continue,
		"--resume": &flags.ResumeAny, "-r": &flags.ResumeAny,
		"--no-session": &flags.NoSession,
		"--no-tools":   &flags.NoTools, "-nt": &flags.NoTools,
		"--no-builtin-tools": &flags.NoBuiltinTools, "-nbt": &flags.NoBuiltinTools,
		"--no-skills": &flags.NoSkills, "-ns": &flags.NoSkills,
		"--no-prompt-templates": &flags.NoPromptTemplates, "-np": &flags.NoPromptTemplates,
		"--no-themes":        &flags.NoThemes,
		"--no-context-files": &flags.NoContextFiles, "-nc": &flags.NoContextFiles,
		"--offline": &flags.Offline,
		"--gtw":     &flags.GTW,
		"--verbose": &flags.Verbose,
	}
	// Value flags consume the next argument; without one they fall through
	// to the switch and are reported as unknown options.
	stringFlags := map[string]*string{
		"--model": &flags.Model, "--provider": &flags.Provider, "--api-key": &flags.APIKey,
		"--system-prompt": &flags.SystemPrompt, "--session": &flags.Session, "--fork": &flags.Fork,
		"--session-dir": &flags.SessionDir, "--session-id": &flags.SessionID, "--export": &flags.Export,
	}
	listFlags := map[string]*[]string{
		"--append-system-prompt": &flags.AppendSystemPrompt, "--skill": &flags.Skills,
		"--prompt-template": &flags.PromptTemplates, "--theme": &flags.Themes,
	}
	// Comma-separated list flags; empty items are dropped.
	csvFlags := map[string]*[]string{
		"--tools": &flags.Tools, "-t": &flags.Tools,
		"--exclude-tools": &flags.ExcludeTools, "-xt": &flags.ExcludeTools,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if p, ok := boolFlags[arg]; ok {
			*p = true
			continue
		}
		if i+1 < len(args) {
			if p, ok := stringFlags[arg]; ok {
				i++
				*p = args[i]
				continue
			}
			if p, ok := listFlags[arg]; ok {
				i++
				*p = append(*p, args[i])
				continue
			}
			if p, ok := csvFlags[arg]; ok {
				i++
				for item := range strings.SplitSeq(args[i], ",") {
					if item = strings.TrimSpace(item); item != "" {
						*p = append(*p, item)
					}
				}
				continue
			}
		}
		switch {
		case arg == "--thinking" && i+1 < len(args):
			i++
			level := args[i]
			if validThinkingLevels[level] {
				flags.Thinking = level
			} else {
				flags.Diagnostics = append(flags.Diagnostics, argDiagnostic{Type: "warning", Message: fmt.Sprintf("Invalid thinking level \"%s\". Valid values: off, minimal, low, medium, high, xhigh, max", level)})
			}
		case arg == "--print" || arg == "-p":
			flags.Print = " " // sentinel: print mode enabled
			// -p may optionally consume the next positional arg as the prompt
			// (e.g. `wopr -p "my prompt"`): if the next arg exists, isn't an
			// @file, and isn't a flag (unless it starts with "---"), consume it
			// as a message.
			if i+1 < len(args) {
				next := args[i+1]
				if !strings.HasPrefix(next, "@") && (!strings.HasPrefix(next, "-") || strings.HasPrefix(next, "---")) {
					i++
					flags.Args = append(flags.Args, args[i])
				}
			}
		case arg == "--name" || arg == "-n":
			if i+1 < len(args) {
				i++
				flags.Name = args[i]
			} else {
				flags.Diagnostics = append(flags.Diagnostics, argDiagnostic{Type: "error", Message: "--name requires a value"})
			}
		case arg == "--list-models":
			// Optional search pattern (not a flag or @file).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				i++
				flags.ListModels = args[i]
			} else {
				flags.ListModelsAll = true
			}
		case arg == "--approve" || arg == "-a":
			flags.ProjectTrustOverride = new(true)
		case arg == "--no-approve" || arg == "-na":
			flags.ProjectTrustOverride = new(false)
		case arg == "--use-theme":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				flags.Diagnostics = append(flags.Diagnostics, argDiagnostic{Type: "error", Message: "--use-theme requires a theme name"})
			} else {
				i++
				flags.UseTheme = args[i]
			}
		case arg == "--tui-mode":
			// Fullscreen is the only mode; the flag and its value are ignored.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case arg == "--mode":
			switch {
			case i+1 >= len(args) || strings.HasPrefix(args[i+1], "-"):
				flags.Diagnostics = append(flags.Diagnostics, argDiagnostic{Type: "error", Message: "--mode requires text, json, or rpc"})
			case args[i+1] == "text" || args[i+1] == "json" || args[i+1] == "rpc":
				i++
				flags.Mode = args[i]
			default:
				i++
				flags.Diagnostics = append(flags.Diagnostics, argDiagnostic{Type: "error", Message: fmt.Sprintf("Invalid mode \"%s\". Valid values: text, json, rpc", args[i])})
			}
		case strings.HasPrefix(arg, "@"):
			flags.FileArgs = append(flags.FileArgs, strings.TrimPrefix(arg, "@"))
		case arg == "--":
			// End of options: the rest are message arguments.
			flags.Args = append(flags.Args, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(arg, "-"):
			option, _, _ := strings.Cut(arg, "=")
			if flags.UnknownOption == "" {
				flags.UnknownOption = option
			}
			flags.Diagnostics = append(flags.Diagnostics, argDiagnostic{Type: "error", Message: "Unknown option: " + option})
		default:
			flags.Args = append(flags.Args, arg)
		}
	}
	return flags
}

//go:embed help.txt
var helpText string

// woprCommands lists the commands printHelp inserts after the Commands
// section of helpText.
const woprCommands = `WOPR Commands:
  wopr verify [path...]          Verify this binary and downloaded files
  wopr status [--json]           Show Resources
  wopr docs [show <topic>]       Read the local WOPR documentation
  wopr version                   Show WOPR, Go, and platform versions

WOPR keeps its configuration in ~/.wopr.

`

// printHelp prints helpText with woprCommands after its Commands section.
// Section headers are bold on a terminal.
func printHelp(w io.Writer, color bool) {
	sections := strings.SplitAfter(helpText, "\n\n")
	var out strings.Builder
	for _, section := range sections {
		out.WriteString(section)
		if strings.HasPrefix(section, "Commands:\n") {
			out.WriteString(woprCommands)
		}
	}
	text := out.String()
	if color {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if helpHeader.MatchString(line) {
				lines[i] = "\x1b[1m" + line + "\x1b[22m"
			}
		}
		if name, rest, ok := strings.Cut(lines[0], " - "); ok {
			lines[0] = "\x1b[1m" + name + "\x1b[22m - " + rest
		}
		text = strings.Join(lines, "\n")
	}
	_, _ = io.WriteString(w, text)
}

var helpHeader = regexp.MustCompile(`^[A-Z][A-Za-z ]*:$`)

// validateForkFlags reports that --fork
// cannot be combined with --session, --continue, --resume or --no-session.
func validateForkFlags(flags CLIFlags) []argDiagnostic {
	if flags.Fork == "" {
		return nil
	}
	var conflicts []string
	if flags.Session != "" {
		conflicts = append(conflicts, "--session")
	}
	if flags.Continue {
		conflicts = append(conflicts, "--continue")
	}
	if flags.ResumeAny {
		conflicts = append(conflicts, "--resume")
	}
	if flags.NoSession {
		conflicts = append(conflicts, "--no-session")
	}
	if len(conflicts) == 0 {
		return nil
	}
	return []argDiagnostic{{Type: "error", Message: "--fork cannot be combined with " + strings.Join(conflicts, ", ")}}
}
