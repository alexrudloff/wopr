package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/export"
	"github.com/alexrudloff/wopr/internal/codingagent/prompts"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/internal/profiling"
	"github.com/alexrudloff/wopr/internal/woprdocs"
	"github.com/alexrudloff/wopr/tui"
)

// Version is wopr's release version. The literal lives in
// coding/version/version.go.
const Version = coding.Version

// Build is the git commit/tag, set via -ldflags at build time.
var Build = "dev"

func buildIdentity() string {
	if Build != "dev" {
		return Build
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Build
	}
	return resolveBuildIdentity(Build, info.Main.Version)
}

func resolveBuildIdentity(linkerBuild, moduleVersion string) string {
	if linkerBuild != "dev" {
		return linkerBuild
	}
	if moduleVersion == "" || moduleVersion == "(devel)" {
		return linkerBuild
	}
	return moduleVersion
}

func detailedVersionString() string {
	return fmt.Sprintf("wopr: %s\ngo: %s\nplatform: %s/%s\nbuild: %s",
		Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, buildIdentity())
}

// ─── Resource Loading ────────────────────────────────────────────────────────────

// loadSkills loads every skill named via --skill. A missing skill is a
// hard error because the user explicitly asked for it. Lookups go through
// the wopr config tree (~/.wopr/skills).
func loadSkills(skillInputs []string, noSkills bool) ([]*codingagent.SkillDef, error) {
	if noSkills || len(skillInputs) == 0 {
		return nil, nil
	}
	var skillDefs []*codingagent.SkillDef
	for _, input := range skillInputs {
		if input == "" {
			continue
		}
		if _, err := os.Stat(input); err == nil {
			defs, err := codingagent.LoadSkillsFromPath(input)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: skill %s: %v\n", input, err)
			}
			for _, def := range defs {
				for _, diagnostic := range codingagent.SkillDiagnostics(def) {
					fmt.Fprintf(os.Stderr, "warning: %s: %s\n", def.Path, diagnostic)
				}
				if strings.TrimSpace(def.Description) != "" {
					skillDefs = append(skillDefs, def)
				}
			}
			continue
		}
		def, err := codingagent.LoadSkill(codingagent.DefaultSkillsDir(), input)
		if err != nil {
			return skillDefs, fmt.Errorf("--skill %q: %w", input, err)
		}
		for _, diagnostic := range codingagent.SkillDiagnostics(def) {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", def.Path, diagnostic)
		}
		if strings.TrimSpace(def.Description) != "" {
			skillDefs = append(skillDefs, def)
		}
	}
	return codingagent.DeduplicateSkills(skillDefs), nil
}

// ─── Initial Message ──────────────────────────────────────────────────────────

// readPipedStdin returns piped stdin content, trimmed. It returns "" when
// stdin is a terminal or the content is blank.
func readPipedStdin() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// buildInitialMessage combines stdin content, @file text, and the first CLI
// message into the initial prompt, and returns the remaining CLI messages,
// which are sent one by one after it. The parts are concatenated in order with no separator, and image files are
// returned as separate content blocks.
func buildInitialMessage(messages []string, fileText string, fileImages []ai.ImageContent, stdinContent string) (string, []ai.ImageContent, []string) {
	var parts []string
	if stdinContent != "" {
		parts = append(parts, stdinContent)
	}
	if fileText != "" {
		parts = append(parts, fileText)
	}
	if len(messages) > 0 {
		parts = append(parts, messages[0])
		messages = messages[1:]
	}
	var images []ai.ImageContent
	if len(fileImages) > 0 {
		images = fileImages
	}
	return strings.Join(parts, ""), images, slices.Clone(messages)
}

// prepareInitialMessage processes the @file arguments and builds the initial
// prompt.
func prepareInitialMessage(cwd string, messages, fileArgs []string, stdinContent string) (string, []ai.ImageContent, []string, error) {
	if len(fileArgs) == 0 {
		initial, images, rest := buildInitialMessage(messages, "", nil, stdinContent)
		return initial, images, rest, nil
	}
	processed, err := codingagent.ProcessCLIFileArguments(fileArgs, cwd)
	if err != nil {
		return "", nil, nil, err
	}
	initial, images, rest := buildInitialMessage(messages, processed.Text, processed.Images, stdinContent)
	return initial, images, rest, nil
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	// WOPR_PROFILE (internal/profiling) is read once here. Unset, it costs one lookup.
	stopProfiles = profiling.Start()
	defer stopProfiles()

	setupCli()

	if len(os.Args) > 1 {
		args := os.Args[1:]
		for _, run := range []func([]string) int{
			runAuthCommand,
			runLoginCommand,
			runStatusCommand,
			func(args []string) int { return runVerifyCommand(args, os.Stdout, os.Stderr) },
			runUpdateCommand,
			func(args []string) int { return woprdocs.RunCommand(args, os.Stdout, os.Stderr) },
		} {
			if code := run(args); code >= 0 {
				exitProcess(code)
			}
		}
		switch args[0] {
		case "config":
			exitProcess(runConfigCommand(args[1:]))
		case "version":
			fmt.Println(detailedVersionString())
			exitProcess(0)
		}
	}

	// `wopr setup` starts the interactive UI on the setup screen.
	runSetup := len(os.Args) > 1 && os.Args[1] == "setup"
	if runSetup {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	flags := parseFlags(os.Args[1:])
	trace.Mark("flags-parsed")
	if reportArgDiagnostics(os.Stderr, flags.Diagnostics, term.IsTerminal(int(os.Stderr.Fd()))) {
		exitProcess(1)
	}

	// Validate and normalize --name once; applied to whichever session is
	// created (interactive or print).
	var sessionName string
	if flags.Name != "" {
		sessionName = strings.TrimSpace(flags.Name)
		if sessionName == "" {
			fatalf("error: --name requires a non-empty value")
		}
	}

	// --session-id conflict check.
	if flags.SessionID != "" {
		// Validate format.
		if !isValidSessionID(flags.SessionID) {
			fatalf("error: session id must be non-empty, contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character")
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
		if len(conflicts) > 0 {
			fatalf("error: --session-id cannot be combined with %s", strings.Join(conflicts, ", "))
		}
	}

	if flags.Version {
		fmt.Println(Version)
		exitProcess(0)
	}

	if flags.Help {
		printHelp(os.Stdout, term.IsTerminal(int(os.Stdout.Fd())))
		exitProcess(0)
	}

	// --offline: set env var so downstream code respects it.
	if flags.Offline {
		_ = os.Setenv("WOPR_OFFLINE", "1")
	}

	// --list-models is handled below, after the built-in llama.cpp provider
	// registers, so its models appear in the catalog.

	// --export: export session file to HTML and exit.
	if flags.Export != "" {
		outputPath := ""
		if len(flags.Args) > 0 {
			outputPath = flags.Args[0]
		}
		result, err := export.ExportFromFile(flags.Export, outputPath)
		if err != nil {
			fatalf("Error: %v", err)
		}
		fmt.Fprintf(os.Stderr, "Exported to: %s\n", result)
		exitProcess(0)
	}

	// Fork flags are validated after --version and --export.
	if reportArgDiagnostics(os.Stderr, validateForkFlags(flags), term.IsTerminal(int(os.Stderr.Fd()))) {
		exitProcess(1)
	}

	initialCWD, err := os.Getwd()
	if err != nil {
		fatalf("error: get cwd: %v", err)
	}

	// Set up context with signal handling.
	//
	// SIGTERM cancels the process. Interactive SIGINT aborts the current
	// operation; print mode installs its own SIGINT handler.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		if sysSig, ok := sig.(syscall.Signal); ok {
			receivedTerminationSignal.Store(int32(sysSig))
		}
		cancel()
	}()

	// Streaming HTTP clients are built on Go's net/http with no overall client
	// timeout and ResponseHeaderTimeout explicitly disabled for long-lived SSE
	// streams; provider-specific deadlines still come from context cancellation.

	// Resolve directories.
	// Working directory comes from os.Getwd() (no CLI override).
	cwd := initialCWD
	agentDir := codingagent.AgentDir()

	// Resolve the selected Session and its runtime cwd before constructing any
	// cwd-bound settings, Packages, Resources, or models.
	// Startup-project settings are used only for sessionDir selection here.
	startupSettingsManager := codingagent.NewSettingsManager(cwd, agentDir)
	startupSettingsDiagnostics := codingagent.CollectSettingsDiagnostics(startupSettingsManager)
	sessionDir, err := resolveSessionDir(flags.SessionDir, startupSettingsManager)
	if err != nil {
		fatalf("error: %v", err)
	}
	startupUIOpts := codingagent.StartupUIOptions{
		AgentDir:   agentDir,
		Settings:   startupSettingsManager.GetGlobalSettings(),
		ThemePaths: collectStartupThemePaths(initialCWD, agentDir, startupSettingsManager),
	}
	if flags.ResumeAny {
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			exitProcess(0)
		}
		manager := newSessionManagerWithDir(initialCWD, sessionDir)
		selected, ok, selectErr := codingagent.SelectStartupSession(
			manager.ListCurrentSessions,
			manager.ListAllSessions,
			startupUIOpts,
		)
		if selectErr != nil {
			fatalf("error: select session: %v", selectErr)
		}
		if !ok {
			// Faint only when color output is allowed.
			msg := "No session selected"
			if os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" {
				msg = "\x1b[2m" + msg + "\x1b[22m"
			}
			_, _ = fmt.Fprintln(os.Stdout, msg)
			exitProcess(0)
		}
		flags.ResumeAny = false
		flags.Session = selected
	}
	startupSession, err := resolveStartupSessionSelection(flags, initialCWD, sessionDir)
	if err != nil {
		fatalf("error: %v", err)
	}
	if crossProject := startupSession.crossProject; crossProject != nil {
		confirmed, confirmErr := confirmCrossProjectSession(os.Stdin, os.Stdout, crossProject.cwd)
		if confirmErr != nil {
			fatalf("error: confirm cross-project session: %v", confirmErr)
		}
		if !confirmed {
			_, _ = fmt.Fprintln(os.Stdout, "Aborted.")
			exitProcess(0)
		}
		manager := newSessionManagerWithDir(startupSession.runtimeCWD, sessionDir)
		forked, forkErr := manager.ForkFromFile(crossProject.path)
		if forkErr != nil {
			fatalf("error: fork session: %v", forkErr)
		}
		startupSession.forkPath = forked.Path()
		startupSession.crossProject = nil
	}
	if issue := startupSession.missingCWD; issue != nil {
		if processAppMode(flags) != appModeInteractive {
			fatalf("%s", issue.Error())
		}
		selected, ok, selectErr := codingagent.ShowStartupSelector(
			issue.prompt(),
			[]string{"Continue", "Cancel"},
			startupUIOpts,
		)
		if selectErr != nil {
			fatalf("error: select session cwd: %v", selectErr)
		}
		if !ok || selected != 0 {
			exitProcess(0)
		}
		startupSession.runtimeCWD = issue.fallbackCWD
		startupSession.missingCWD = nil
	}
	if flags.Continue && startupSession.resumePath == "" {
		manager := newSessionManagerWithDir(initialCWD, sessionDir)
		fmt.Fprintf(os.Stderr, "warning: no prior session in %s; starting fresh\n", manager.SessionDir())
	}
	cwd = startupSession.runtimeCWD
	sessionDir = startupSession.sessionDir
	resourceFlags := resolveCLIResourceFlags(flags, initialCWD)
	hasTrustResources := codingagent.HasTrustRequiringProjectResources(cwd)
	projectTrusted := flags.ProjectTrustOverride != nil && *flags.ProjectTrustOverride
	if flags.ProjectTrustOverride == nil && !hasTrustResources {
		projectTrusted = true
	}

	// Construct the SDK Services container. Matches what library
	// consumers of github.com/alexrudloff/wopr/coding will do;
	// the binary uses the same path so SDK + CLI share the same
	// startup semantics.
	trace.Mark("pre-services")
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            cwd,
		AgentDir:       agentDir,
		ProjectTrusted: new(projectTrusted),
	})
	if err != nil {
		fatalf("wopr: services init: %v", err)
	}
	trace.Mark("services-created")
	llamaHost := startBuiltInLlama(ctx, services)
	// --use-theme applies to this run only.
	services.SettingsManager().ApplyOverrides(codingagent.Settings{Theme: flags.UseTheme})
	settings := services.Settings()

	if flags.ProjectTrustOverride == nil && hasTrustResources {
		var trustUI trustPrompter
		interactiveTrust := processAppMode(flags) == appModeInteractive && !flags.Help && flags.ListModels == "" && !flags.ListModelsAll
		if interactiveTrust {
			trustUI = newStartupTrustUI(startupUIOpts)
		}
		globalDefaultTrust := startupSettingsManager.GetGlobalSettings().DefaultProjectTrust
		if globalDefaultTrust == "" {
			globalDefaultTrust = "ask"
		}
		projectTrusted, err = resolveProjectTrusted(ctx, projectTrustResolutionOptions{
			CWD:     cwd,
			Store:   codingagent.NewProjectTrustStore(agentDir),
			Default: globalDefaultTrust,
			UI:      trustUI,
		})
		if err != nil {
			fatalf("error: resolve project trust: %v", err)
		}
		services.SettingsManager().SetProjectTrusted(projectTrusted)
		settings = services.Settings()
		trace.Mark("trust-resolved")
	}
	// Apply the terminal capability overrides once the runtime settings exist
	// and before any mode runs.
	tui.SetCapabilityOverrides(settings.GetTerminalCapabilityOverrides())
	// Startup and runtime settings managers can report the same file error,
	// so the combined list is deduplicated.
	startupDiagnostics := codingagent.DeduplicateDiagnostics(append(startupSettingsDiagnostics, codingagent.CollectSettingsDiagnostics(services.SettingsManager())...))

	promptPaths := collectPromptPaths(cwd, agentDir, services.SettingsManager(), resourceFlags, projectTrusted)
	themePaths := collectThemePaths(cwd, agentDir, services.SettingsManager(), resourceFlags, projectTrusted)
	skillInputs := collectSkillInputs(cwd, agentDir, services.SettingsManager(), resourceFlags, projectTrusted)

	trace.Mark("services-init")
	_ = woprdocs.EnsureSynced(codingagent.ConfigRoot())
	registry := services.Registry()

	// --list-models: print the model catalog and exit before model resolution
	// and the session UI.
	if flags.ListModels != "" || flags.ListModelsAll {
		codingagent.ReportDiagnostics(startupSettingsDiagnostics)
		printModelList(registry.ModelRegistry, agentDir, flags.ListModels)
		exitProcess(0)
	}

	// A model named on the command line is the user's pick: it pins the
	// orchestrator, and routing keeps routing subagents.
	pinModel := flags.Model != "" && !strings.EqualFold(flags.Model, "auto")

	// Resolve model: --model flag > settings.defaultModel
	trace.Mark("pre-model")
	selected, err := selectStartupModel(startupModelOptions{
		CLIProvider: flags.Provider,
		CLIModel:    flags.Model,
		CLIThinking: flags.Thinking,
		APIKey:      flags.APIKey,
	}, settings, services)
	// Surface model-resolution warnings (e.g. an unknown model under a known
	// provider that fell back to the provider's default caps) as yellow "Warning: …" on stderr, before
	// the TUI takes over.
	for _, warning := range selected.Warnings {
		printModelDiagnostic(warning)
	}
	if err != nil {
		fatalf("error: %v", err)
	}
	model, specThinking := selected.Model, selected.Thinking
	// Interactive mode tolerates a nil model and
	// emits a "Warning: No models available." diagnostic that the TUI
	// renders alongside the welcome banner. Print/JSON/RPC modes re-check
	// at their call sites and fail.
	noModelWarning := ""
	if model == nil {
		noModelWarning = codingagent.FormatNoModelsAvailableMessage()
	}
	// Thinking override cascade: --thinking flag > model-spec ":medium"
	if specThinking != "" && flags.Thinking == "" {
		flags.Thinking = specThinking
	}

	trace.Mark("model-resolved")

	// Load the collected skills. --no-skills already emptied the discovered
	// inputs, so the explicit inputs always load here.
	skillDefs, err := loadSkills(skillInputs, false)
	if err != nil {
		fatalf("error: %v", err)
	}

	// Build the default system prompt. The tool list and hints
	// reflect the actual tools we'll hand to the agent loop.
	// registryToolNames is the full set of built-in tools that exist and can
	// be activated via --tools. agentToolNames is the ACTIVE set advertised in
	// the system prompt; it defaults to read/bash/edit/write plus task (the
	// subagent tool) and the web and MCP tools, with powershell/grep/find/ls
	// registered but opt-in. The session installs web_search only when a
	// search backend is configured and mcp only when a server is.
	registryToolNames := tools.BuiltinToolNames()
	agentToolNames := []string{"read", "bash", "edit", "write", "task", "web_fetch", "web_search", "mcp"}
	// The defaultTools setting replaces the default active set.
	if settings.DefaultTools != nil {
		agentToolNames = append([]string(nil), settings.DefaultTools...)
	}
	allowed := map[string]struct{}(nil)
	// activeBuiltin, when non-nil, restricts which built-in tools are active
	// without gating custom tools (AllowedTools gates everything). nil =
	// all built-in tools. Used to apply the default-active set.
	var activeBuiltin map[string]struct{}
	skipBuiltinTools := flags.NoBuiltinTools
	if flags.NoBuiltinTools {
		agentToolNames = nil
	}
	// --no-tools disables all tools (empty allowlist).
	// --tools <list> restricts to those names.
	switch {
	case flags.NoTools:
		allowed = make(map[string]struct{}) // empty = block all
		agentToolNames = nil
		skipBuiltinTools = false
	case len(flags.Tools) > 0:
		allowed = make(map[string]struct{}, len(flags.Tools))
		for _, t := range flags.Tools {
			allowed[t] = struct{}{}
		}
		if !flags.NoBuiltinTools {
			// Advertise the requested built-in tools in registry order.
			// AllowedTools does the real activation gating.
			filtered := make([]string, 0, len(flags.Tools))
			for _, n := range registryToolNames {
				if _, ok := allowed[n]; ok {
					filtered = append(filtered, n)
				}
			}
			agentToolNames = filtered
		}
	default:
		if !flags.NoBuiltinTools {
			// Default active set excludes grep/find/ls.
			// AllowedTools stays nil so custom tools remain active.
			activeBuiltin = make(map[string]struct{}, len(agentToolNames))
			for _, t := range agentToolNames {
				activeBuiltin[t] = struct{}{}
			}
		}
	}
	// --exclude-tools / -xt: deny specific tools. The excluded set is both
	// removed from the system-prompt advertisement (agentToolNames) and
	// applied as a denylist at the agent loop (excludedTools), so an excluded
	// tool is non-callable, not merely hidden. Gates built-in AND custom
	// tools.
	var excludedTools map[string]struct{}
	if len(flags.ExcludeTools) > 0 {
		excludedTools = make(map[string]struct{}, len(flags.ExcludeTools))
		for _, t := range flags.ExcludeTools {
			excludedTools[t] = struct{}{}
		}
		filtered := agentToolNames[:0]
		for _, n := range agentToolNames {
			if _, ok := excludedTools[n]; !ok {
				filtered = append(filtered, n)
			}
		}
		agentToolNames = filtered
	}
	trace.Mark("pre-system-prompt")
	projectCtxFiles := loadContextFiles(cwd, agentDir, flags.NoContextFiles)
	promptOptions := systemPromptOptions(cwd, agentDir, projectTrusted, flags, agentToolNames, skillDefs, projectCtxFiles)
	systemPromptSections := prompts.BuildSystemPromptSections(promptOptions)
	systemPrompt := prompts.BuildDefaultPrompt(promptOptions)

	if err := configureHTTPTransportFromSettings(services.SettingsManager()); err != nil {
		fatalf("error: configure HTTP transport: %v", err)
	}

	// Every mode starts its Session from these options, so tool defaults,
	// prompts and model selection cannot drift between modes.
	resumePath := startupSession.resumePath
	if startupSession.forkPath != "" {
		resumePath = startupSession.forkPath
	}
	startOpts := coding.SessionStartOptions{
		Model:                model,
		ThinkingLevel:        flags.Thinking,
		SystemPrompt:         systemPrompt,
		SystemPromptSections: systemPromptSections,
		AllowedTools:         allowed,
		ActiveBuiltinTools:   activeBuiltin,
		ExcludedTools:        excludedTools,
		SkipBuiltinTools:     skipBuiltinTools,
		PinModel:             pinModel,
		ResumePath:           resumePath,
		SessionDir:           sessionDir,
		SessionID:            flags.SessionID,
		NoSession:            flags.NoSession,
	}

	// RPC mode: headless JSONL command/event loop.
	// Takes over stdin/stdout; no interactive TUI.
	// Must be dispatched BEFORE buildInitialMessage which reads piped
	// stdin: consuming it would starve the RPC command reader.
	if flags.Mode == "rpc" {
		promptResult := codingagent.LoadPromptTemplates("", "", promptPaths...)
		promptTemplates := promptResult.Templates
		for _, diagnostic := range promptResult.Diagnostics {
			startupDiagnostics = append(startupDiagnostics, codingagent.AgentSessionRuntimeDiagnostic{Type: diagnostic.Type, Message: diagnostic.Path + ": " + diagnostic.Message})
		}
		rpcResources := rpcModeResources{
			Services:        services,
			Session:         startOpts,
			Llama:           llamaHost,
			CWD:             cwd,
			AgentDir:        agentDir,
			PromptTemplates: promptTemplates,
			Skills:          skillDefs,
			SourceInfo:      resourceSourceInfoProvider(cwd, agentDir, services.SettingsManager(), resourceFlags)(),
		}
		codingagent.ReportDiagnostics(startupDiagnostics)
		exitProcess(runRPCMode(ctx, flags, rpcResources))
	}

	// Build the initial message from positional args and piped stdin.
	initialMessage, initialImages, extraMessages, err := prepareInitialMessage(initialCWD, flags.Args, flags.FileArgs, readPipedStdin())
	if err != nil {
		fatalf("Error: %v", err)
	}

	// Print mode
	// --print, --mode json, or a stdin or stdout that is not a terminal runs
	// print mode.
	if processAppMode(flags) != appModeInteractive {
		codingagent.ReportDiagnostics(startupDiagnostics)
		if model == nil {
			fatalf("error: no model specified. Use --model, run `wopr login github-copilot`, or set OPENAI_API_KEY")
		}
		// The print/JSON prompt comes from positional args, @files, and stdin.
		// --print is a bare boolean; the prompt is in initialMessage, which
		// may be empty (then nothing is sent).
		mode := "text"
		if processAppMode(flags) == appModeJSON {
			mode = "json"
		}
		host := printModeRuntime{
			Services:    services,
			Session:     startOpts,
			SessionName: sessionName,
		}
		if err := runPrintMode(ctx, host, printModeOptions{Mode: mode, Messages: extraMessages, InitialMessage: initialMessage, InitialImages: initialImages}); err != nil {
			// A run stopped by a termination signal reports 128+signum and
			// stays quiet.
			if signalErr, ok := errors.AsType[*signalExitError](err); ok {
				exitProcess(signalErr.ExitCode())
			}
			if !errors.Is(err, errPrintModeHandled) {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			exitProcess(1)
		}
		return
	}

	trace.Mark("pre-session")
	codingSess, err := coding.StartSession(services, startOpts)
	if err != nil {
		fatalf("error: construct session: %v", err)
	}
	trace.Mark("session-created")
	defer func() { _ = codingSess.Close() }()
	// A resumed session restores the model it was using.
	if restored := codingSess.Model(); restored != nil {
		model = restored
	}
	// Persist --name to session_info so the display name survives resume.
	if sessionName != "" {
		if err := codingSess.SetSessionName(sessionName); err != nil {
			fatalf("error: set session name: %v", err)
		}
	}
	requestAuthRuntime, err := codingagent.NewRequestAuthRuntime(ctx, codingagent.RequestAuthRuntimeOptions{
		Credentials: services.Auth(),
		AgentDir:    agentDir,
	})
	if err != nil {
		fatalf("error: construct request auth runtime: %v", err)
	}
	iopts := codingagent.InteractiveOptions{
		ContextUsage: func() (*int, int) {
			usage := codingSess.ContextUsage()
			if usage == nil {
				return nil, 0
			}
			return usage.Tokens, usage.ContextWindow
		},
		CWD:                cwd,
		AgentDir:           agentDir,
		SessionDir:         sessionDir,
		Model:              model,
		NoModelWarning:     noModelWarning,
		RunSetup:           runSetup,
		ModelFromFlag:      pinModel,
		StartupDiagnostics: startupDiagnostics,
		SettingsManager:    services.SettingsManager(),
		SystemPrompt:       systemPrompt,
		AllowedTools:       allowed,
		ActiveBuiltinTools: activeBuiltin,
		ExcludedTools:      excludedTools,
		NoBuiltinTools:     skipBuiltinTools,
		PromptPaths:        promptPaths,
		ThemePaths:         themePaths,
		NoPromptTemplates:  flags.NoPromptTemplates,
		NoThemes:           flags.NoThemes,
		InitialMessage:     initialMessage,
		InitialImages:      initialImages,
		InitialMessages:    extraMessages,
		AppVersion:         Version,
		BinaryUpdateChecker: func() *codingagent.BinaryUpdate {
			if IsOfflineModeEnabled() {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			return codingagent.CheckForBinaryUpdate(ctx, codingagent.DefaultReleaseSource(&http.Client{Timeout: 6 * time.Second}), Version)
		},
		ReleaseSource:              newReleaseSource,
		ResourceSourceInfoProvider: resourceSourceInfoProvider(cwd, agentDir, services.SettingsManager(), flags),
		ReloadResourceProvider:     reloadResourceSnapshotProvider(cwd, agentDir, services.SettingsManager(), resourceFlags, projectTrusted),
		Verbose:                    flags.Verbose,
		ThinkingLevel:              flags.Thinking,
		Skills:                     skillDefs,
		RebuildSystemPrompt:        systemPromptRebuilder(cwd, agentDir, projectTrusted, flags, agentToolNames),
		SkillPaths:                 skillInputs,
		NoSkills:                   flags.NoSkills,
		ContextFiles:               projectCtxFiles,
		SessionHandle:              codingSess,
		ResumePath:                 resumePath,
		ModelBuilder: func(spec string) (*ai.Model, error) {
			return coding.BuildModel(spec, services)
		},
		RequestAuthRuntime: requestAuthRuntime,
		ModelRegistry:      services.Registry().ModelRegistry,
		Llama:              llamaHost,
		OfflineMode:        IsOfflineModeEnabled(),
		StartupMark:        trace.Mark,
	}
	interactive := codingagent.NewInteractiveMode(iopts)

	trace.Mark("pre-interactive")
	if err := interactive.Run(ctx); err != nil {
		if errors.Is(err, codingagent.ErrInteractiveCrashed) {
			exitProcess(1)
		}
		fatalf("error: %v", err)
	}
	if session, ok := interactive.RestartRequested(); ok {
		if err := restartInto(session); err != nil {
			fatalf("restart: %v (start %s again to use the new version)", err, codingagent.AppName)
		}
	}
}

// toPromptContextFiles converts codingagent.ContextFile to the shape
// expected by prompts.Options.ContextFiles.
func toPromptContextFiles(cfs []codingagent.ContextFile) []struct{ Path, Content string } {
	out := make([]struct{ Path, Content string }, len(cfs))
	for i, cf := range cfs {
		out[i] = struct{ Path, Content string }{Path: cf.Path, Content: cf.Content}
	}
	return out
}

func resolveCLIResourceFlags(flags CLIFlags, launchCWD string) CLIFlags {
	resolve := func(paths []string) []string {
		out := make([]string, len(paths))
		for i, path := range paths {
			out[i] = resolveSettingsPath(launchCWD, path)
		}
		return out
	}
	flags.Skills = resolve(flags.Skills)
	flags.PromptTemplates = resolve(flags.PromptTemplates)
	flags.Themes = resolve(flags.Themes)
	return flags
}

func resolveSessionDir(flagValue string, sm *codingagent.SettingsManager) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if envValue := os.Getenv(codingagent.ENV_SESSION_DIR); envValue != "" {
		return codingagent.ExpandTildePath(envValue), nil
	}
	if sm != nil {
		return sm.GetSessionDir()
	}
	return "", nil
}

// configureHTTPTransportFromSettings applies the resolved provider request
// settings to the ai package: the effective per-request timeout as the HTTP
// idle timeout, and maxRetries/maxRetryDelayMs onto the provider retry
// transport.
func configureHTTPTransportFromSettings(sm *codingagent.SettingsManager) error {
	if sm == nil {
		return ai.ConfigureHTTPIdleTimeout(ai.DefaultHTTPIdleTimeoutMs)
	}
	// retry.provider.timeoutMs overrides httpIdleTimeoutMs when set.
	timeoutMs, err := sm.GetProviderRequestTimeoutMs()
	if err != nil {
		return err
	}
	if err := ai.ConfigureHTTPIdleTimeout(timeoutMs); err != nil {
		return err
	}
	// retry.provider.maxRetries / maxRetryDelayMs drive the provider retry
	// transport.
	pr := sm.GetProviderRetrySettings()
	return ai.ConfigureProviderRetry(pr.MaxRetries, pr.MaxRetryDelayMs)
}

func newSessionManagerWithDir(cwd, sessionDir string) *codingagent.SessionManager {
	if sessionDir != "" {
		return codingagent.NewSessionManagerWithDir(cwd, sessionDir)
	}
	return codingagent.NewSessionManager(cwd)
}

// isValidSessionID validates session ID format.
var validSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func isValidSessionID(id string) bool {
	return validSessionIDPattern.MatchString(id)
}
