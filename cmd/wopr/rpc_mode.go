// Headless JSONL command and event loop.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
	codingexport "github.com/alexrudloff/wopr/internal/codingagent/export"
	"github.com/alexrudloff/wopr/internal/codingagent/llama"
)

// rpcModeResources is what main's shared startup assembled for RPC mode.
type rpcModeResources struct {
	Services        *coding.Services
	Session         coding.SessionStartOptions
	Llama           *llama.Host
	CWD             string
	AgentDir        string
	PromptTemplates []codingagent.PromptTemplate
	Skills          []*codingagent.SkillDef
	SourceInfo      map[string]codingagent.ResourceSourceInfo
}

type rpcCommandCatalog struct {
	// llama backs the built-in /llama command; notify carries its notices to
	// the RPC client.
	llama           *llama.Host
	notify          func(message, kind string)
	promptTemplates []codingagent.PromptTemplate
	skills          []*codingagent.SkillDef
	cwd             string
	agentDir        string
	sourceInfo      map[string]codingagent.ResourceSourceInfo
}

func (c rpcCommandCatalog) commands() []RPCSlashCommand {
	commands := make([]RPCSlashCommand, 0)
	if c.llama != nil {
		commands = append(commands, RPCSlashCommand{
			Name: llama.CommandName, Description: llama.CommandDescription, Source: "builtin",
			SourceInfo: RPCSourceInfo{Path: llamaSourcePath, Source: "inline", Scope: "temporary", Origin: "top-level"},
		})
	}
	for _, template := range c.promptTemplates {
		commands = append(commands, RPCSlashCommand{
			Name: template.Name, Description: template.Description, Source: "prompt",
			SourceInfo: c.sourceInfoForPath(template.FilePath, "prompts"),
		})
	}
	for _, skill := range c.skills {
		commands = append(commands, RPCSlashCommand{
			Name: "skill:" + skill.Name, Description: skill.Description, Source: "skill",
			SourceInfo: c.sourceInfoForPath(skill.Path, "skills"),
		})
	}
	return commands
}

// builtinCommand resolves a message that invokes /llama.
func (c rpcCommandCatalog) builtinCommand(message string) (string, string, bool) {
	if !strings.HasPrefix(message, "/") {
		return "", "", false
	}
	requestedNameAndArgs := message[1:]
	requestedName, args, found := strings.Cut(requestedNameAndArgs, " ")
	if !found {
		args = ""
	}
	if c.llama != nil && requestedName == llama.CommandName {
		return llama.CommandName, args, true
	}
	return "", "", false
}

func (c rpcCommandCatalog) expandPrompt(message string) string {
	if expanded, ok := codingagent.ExpandSkillCommand(message, c.skills); ok {
		message = expanded
	}
	if expanded, ok := codingagent.ExpandPromptTemplate(message, c.promptTemplates); ok {
		message = expanded
	}
	return message
}

func (c rpcCommandCatalog) routePrompt(ctx context.Context, message string) (string, bool) {
	if name, args, ok := c.builtinCommand(message); ok {
		return "", c.executeCommand(ctx, name, args)
	}
	return c.expandPrompt(message), false
}

// executeCommand runs a command builtinCommand resolved: /llama runs the
// built-in llama.cpp command with RPC's command context.
func (c rpcCommandCatalog) executeCommand(ctx context.Context, name, _ string) bool {
	if c.llama != nil && name == llama.CommandName {
		_ = c.llama.HandleCommand(llama.CommandContext{Ctx: ctx, Mode: "rpc", Notify: c.notify})
		return true
	}
	return false
}

func (c rpcCommandCatalog) sourceInfoForPath(path, kind string) RPCSourceInfo {
	for _, candidate := range []string{path, filepath.Dir(path)} {
		if info, ok := c.sourceInfo[candidate]; ok {
			scope := info.Scope
			if scope == "" {
				scope = "temporary"
			}
			origin := info.Origin
			if origin == "" {
				origin = "top-level"
			}
			source := info.Source
			if source == "" {
				source = "local"
			}
			baseDir := info.BaseDir
			if baseDir == "" && path != "" {
				baseDir = filepath.Dir(path)
			}
			return RPCSourceInfo{Path: path, Source: source, Scope: scope, Origin: origin, BaseDir: baseDir}
		}
	}
	info := RPCSourceInfo{Path: path, Source: "local", Scope: "temporary", Origin: "top-level"}
	if path != "" {
		info.BaseDir = filepath.Dir(path)
	}
	userRoot := filepath.Join(c.agentDir, kind)
	projectRoot := filepath.Join(c.cwd, codingagent.CONFIG_DIR_NAME, kind)
	switch {
	case isWithin(path, userRoot):
		info.Scope, info.BaseDir = "user", userRoot
	case isWithin(path, projectRoot):
		info.Scope, info.BaseDir = "project", projectRoot
	}
	return info
}

func rpcGetCommandsResponse(id string, catalog rpcCommandCatalog) RPCResponse {
	return rpcSuccess(id, "get_commands", RPCGetCommandsData{Commands: catalog.commands()})
}

// runRPCMode is the entrypoint for `wopr --rpc`.
// It initialises a coding.Session, starts the event forwarder goroutine,
// then reads commands from stdin until EOF.
// Returns 0 on clean shutdown, 1 on fatal error.
func runRPCMode(ctx context.Context, flags CLIFlags, resources rpcModeResources) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var writeMu sync.Mutex
	writeRPC := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		writeJSONLine(os.Stdout, v)
	}

	services := resources.Services
	cwd, agentDir := resources.CWD, resources.AgentDir
	sessionDir := resources.Session.SessionDir
	refreshCatalogsInBackground(ctx, resources.Llama)

	sess, err := coding.StartSession(services, resources.Session)
	if err != nil {
		return failf("wopr --rpc: session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Diagnostic: log actual tool count from the agent.
	fmt.Fprintf(os.Stderr, "wopr --rpc: agent has %d tools (builtins_skipped=%v)\n",
		len(sess.Agent().Tools()), flags.NoBuiltinTools)

	// An unknown model supports only off.
	if resources.Session.Model == nil {
		sess.Agent().SetThinkingLevel(ai.ThinkingOff)
	}

	commandCatalog := rpcCommandCatalog{
		promptTemplates: resources.PromptTemplates, skills: resources.Skills,
		cwd: cwd, agentDir: agentDir, sourceInfo: resources.SourceInfo,
		llama: resources.Llama, notify: rpcNotifier(writeRPC),
	}

	// ── Event forwarder ────────────────────────────────────────────────────
	// Forward AgentSessionEvent JSON objects: RPC mode subscribes to session
	// events and writes each event as a JSONL object; rpcAgentEvent adapts
	// wopr's internal event structs to that wire shape.

	// The Session owns streaming state and abort. promptPending covers the gap
	// between accepting an RPC prompt and its run starting.
	var promptPending atomic.Bool
	streaming := func() bool { return sess.IsStreaming() || promptPending.Load() }
	var promptWG sync.WaitGroup
	var bashWG sync.WaitGroup
	var commandWG sync.WaitGroup
	cycleComplete := make(chan struct{})
	close(cycleComplete)

	handleCycleModel := func(id string, currentSession *coding.Session) {
		models, err := rpcAvailableModels(services, currentSession.Model())
		if err != nil {
			writeRPC(rpcError(id, "cycle_model", err.Error()))
			return
		}
		scoped := models
		if len(scoped) <= 1 {
			writeRPC(rpcSuccessNull(id, "cycle_model"))
			return
		}
		currentIndex := max(slices.IndexFunc(scoped, func(candidate *ai.Model) bool {
			return ai.ModelsAreEqual(candidate, currentSession.Model())
		}), 0)
		next := scoped[(currentIndex+1)%len(scoped)]
		if err := currentSession.CycleToModel(next); err != nil {
			writeRPC(rpcError(id, "cycle_model", err.Error()))
			return
		}
		writeRPC(rpcSuccess(id, "cycle_model", RPCModelCycleResult{
			Model: rpcModelValue(next), ThinkingLevel: currentSession.ThinkingLevel(), IsScoped: false,
		}))
	}

	// settlePrompt aborts and waits: the active run stops,
	// whoever started it, and its agent_settled is on the wire before the
	// caller answers.
	settlePrompt := func() {
		sess.RequestAbort()
		promptWG.Wait()
		_ = sess.WaitForIdle(context.Background())
		_ = sess.FlushEvents(ctx)
	}
	settleSessionWork := func() {
		commandWG.Wait()
		settlePrompt()
		sess.AbortBash()
		bashWG.Wait()
	}

	// eventsDone is closed when the event forwarder exits.
	eventsDone := make(chan struct{})
	eventConversionErr := make(chan error, 1)

	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			if coding.AcknowledgeEvent(ev) {
				continue
			}
			converted, err := rpcAgentEvent(ev)
			if err != nil {
				eventConversionErr <- err
				writeRPC(RPCErrorEvent{Type: "error", Message: "event conversion error: " + err.Error()})
				cancel()
				return
			}
			for _, outEvent := range converted {
				writeRPC(outEvent)
			}
		}
	}()

	// ── Command loop ───────────────────────────────────────────────────────

	inputLines := make(chan []byte, 64)
	inputDone := make(chan error, 1)
	go func() {
		defer close(inputLines)
		err := readJSONLLines(os.Stdin, func(line []byte) bool {
			select {
			case inputLines <- line:
				return true
			case <-ctx.Done():
				return false
			}
		})
		if ctx.Err() != nil {
			inputDone <- nil
			return
		}
		inputDone <- err
	}()

	for line := range inputLines {

		env, parseErr := parseRPCCommand(line)
		if parseErr != nil {
			// A parse error has no request id.
			writeRPC(rpcError("", "parse", "Failed to parse command: "+parseErr.Error()))
			continue
		}

		switch env.Type {
		// ── prompt ──────────────────────────────────────────────────────
		case "prompt":
			cmd, ok := decodeRPC[RPCPromptCommand](env, writeRPC)
			if !ok {
				continue
			}
			if name, args, ok := commandCatalog.builtinCommand(cmd.Message); ok {
				go func(id, commandName, commandArgs string) {
					commandCatalog.executeCommand(ctx, commandName, commandArgs)
					writeRPC(rpcSuccess(id, "prompt", nil))
				}(env.ID, name, args)
				continue
			}
			if sess.IsCompacting() {
				writeRPC(rpcError(env.ID, "prompt", "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry."))
				continue
			}
			images := rpcImages(cmd.Images)
			cmd.Message = commandCatalog.expandPrompt(cmd.Message)
			if streaming() {
				switch cmd.StreamingBehavior {
				case "steer":
					sess.Steer(cmd.Message, images)
				case "followUp":
					sess.FollowUp(cmd.Message, images)
				case "":
					writeRPC(rpcError(env.ID, "prompt", "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message."))
					continue
				default:
					writeRPC(rpcError(env.ID, "prompt", fmt.Sprintf("Invalid streamingBehavior: %s", cmd.StreamingBehavior)))
					continue
				}
				if err := sess.FlushEvents(ctx); err != nil {
					writeRPC(rpcError(env.ID, "prompt", err.Error()))
					continue
				}
				writeRPC(rpcSuccess(env.ID, "prompt", nil))
				continue
			}

			if model := sess.Model(); model == nil {
				writeRPC(rpcError(env.ID, "prompt", codingagent.FormatNoAPIKeyFoundMessage("unknown")))
				continue
			} else {
				providerID := model.ProviderMeta.ProviderID
				if providerID == "" && model.Provider != nil {
					providerID = model.Provider.ID()
				}
				if providerID != "test-faux" && !services.Registry().HasConfiguredAuth(providerID) {
					writeRPC(rpcError(env.ID, "prompt", codingagent.FormatNoAPIKeyFoundMessage(providerID)))
					continue
				}
			}

			// Run Send in a goroutine so we don't block the command reader.
			// The response is written once preflight
			// succeeds, a failure before that is the response, and a failure
			// after it is swallowed (the run's events already report it).
			promptPending.Store(true)
			promptWG.Go(func() {
				accepted := false
				_, sendErr := sess.SendContentWithPreflight(ctx, coding.BuildUserContent(cmd.Message, images), func() {
					accepted = true
					promptPending.Store(false)
					writeRPC(rpcSuccess(env.ID, "prompt", nil))
				})
				if accepted {
					return
				}
				promptPending.Store(false)
				if sendErr != nil {
					writeRPC(rpcError(env.ID, "prompt", sendErr.Error()))
				} else {
					writeRPC(rpcSuccess(env.ID, "prompt", nil))
				}
			})

		// ── abort ────────────────────────────────────────────────────────
		case "abort":
			settlePrompt()
			writeRPC(rpcSuccess(env.ID, "abort", nil))

		case "clear_queue":
			steering, followUp := sess.ClearQueue()
			// Flush the queue_update this emits through the wire before the
			// response (steer/follow_up flush the same way for the same reason).
			if err := sess.FlushEvents(ctx); err != nil {
				writeRPC(rpcError(env.ID, "clear_queue", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "clear_queue", RPCClearQueueData{Steering: steering, FollowUp: followUp}))

		case "new_session":
			cmd, ok := decodeRPC[RPCNewSessionCommand](env, writeRPC)
			if !ok {
				continue
			}
			sessionID, err := codingagent.GenerateSessionID()
			if err != nil {
				writeRPC(rpcError(env.ID, "new_session", err.Error()))
				continue
			}
			var next *codingagent.Session
			if flags.NoSession {
				next = codingagent.NewSession(sessionID, cwd)
			} else {
				next, err = newSessionManagerWithDir(cwd, sessionDir).Create(sessionID, cmd.ParentSession)
				if err != nil {
					writeRPC(rpcError(env.ID, "new_session", err.Error()))
					continue
				}
			}
			if model := sess.Model(); model != nil {
				if err := next.AppendModelSwitch(model.ProviderMeta.ProviderID, model.ID, model.DisplayName); err != nil {
					writeRPC(rpcError(env.ID, "new_session", err.Error()))
					continue
				}
			}
			if err := next.AppendThinkingLevelChange(string(sess.ThinkingLevel())); err != nil {
				writeRPC(rpcError(env.ID, "new_session", err.Error()))
				continue
			}
			settleSessionWork()
			sess.ReplaceInner(next)
			writeRPC(rpcSuccess(env.ID, "new_session", RPCCancelledResult{Cancelled: false}))

		// ── get_commands ───────────────────────────────────────────────
		case "get_commands":
			writeRPC(rpcGetCommandsResponse(env.ID, commandCatalog))

		// ── get_state ────────────────────────────────────────────────────
		case "get_state":
			compactionEnabled := services.SettingsManager().GetCompactionSettings().Enabled
			state := RPCSessionState{
				Model:                 rpcModelValue(sess.Model()),
				ThinkingLevel:         string(sess.ThinkingLevel()),
				IsStreaming:           streaming(),
				IsCompacting:          sess.IsCompacting(),
				SteeringMode:          string(sess.Agent().SteeringMode()),
				FollowUpMode:          string(sess.Agent().FollowUpMode()),
				SessionFile:           sess.Path(),
				SessionID:             sess.ID(),
				SessionName:           sess.SessionName(),
				AutoCompactionEnabled: compactionEnabled,
				MessageCount:          len(sess.Messages()),
				PendingMessageCount:   sess.PendingMessageCount(),
			}
			writeRPC(rpcSuccess(env.ID, "get_state", state))

		// ── set_model ────────────────────────────────────────────────────
		case "set_model":
			cmd, ok := decodeRPC[RPCSetModelCommand](env, writeRPC)
			if !ok {
				continue
			}
			models, err := rpcAvailableModels(services, sess.Model())
			if err != nil {
				writeRPC(rpcError(env.ID, "set_model", err.Error()))
				continue
			}
			i := slices.IndexFunc(models, func(candidate *ai.Model) bool {
				return candidate.ProviderMeta.ProviderID == cmd.Provider && candidate.ID == cmd.ModelID
			})
			if i < 0 {
				writeRPC(rpcError(env.ID, "set_model", fmt.Sprintf("Model not found: %s/%s", cmd.Provider, cmd.ModelID)))
				continue
			}
			newModel := models[i]
			if err := sess.SetModel(newModel); err != nil {
				writeRPC(rpcError(env.ID, "set_model", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "set_model", rpcModelValue(newModel)))

		case "cycle_model":
			commandID := env.ID
			currentSession := sess
			previousCycle := cycleComplete
			cycleComplete = make(chan struct{})
			currentCycle := cycleComplete
			commandWG.Go(func() {
				defer close(currentCycle)
				select {
				case <-ctx.Done():
					return
				case <-previousCycle:
				}
				handleCycleModel(commandID, currentSession)
			})

		// ── compact ─────────────────────────────────────────────────────
		case "compact":
			cmd, ok := decodeRPC[RPCCompactCommand](env, writeRPC)
			if !ok {
				continue
			}
			commandID := env.ID
			customInstructions := cmd.CustomInstructions
			currentSession := sess
			commandWG.Go(func() {
				settlePrompt()
				result, err := currentSession.CompactResult(ctx, customInstructions)
				if err != nil {
					writeRPC(rpcError(commandID, "compact", err.Error()))
					return
				}
				writeRPC(rpcSuccess(commandID, "compact", rpcCompactionResult(result)))
			})

		// ── set_auto_compaction ──────────────────────────────────────────
		case "set_auto_compaction":
			cmd, ok := decodeRPC[RPCSetAutoCompactionCommand](env, writeRPC)
			if !ok {
				continue
			}
			if err := services.SettingsManager().UpdateGlobal(func(s *codingagent.Settings) {
				if s.Compaction == nil {
					s.Compaction = &codingagent.CompactionSettingsJSON{}
				}
				s.Compaction.Enabled = &cmd.Enabled
			}); err != nil {
				writeRPC(rpcError(env.ID, "set_auto_compaction", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "set_auto_compaction", nil))

		// ── get_available_models ─────────────────────────────────────────
		case "get_available_models":
			models, err := rpcAvailableModels(services, sess.Model())
			if err != nil {
				writeRPC(rpcError(env.ID, "get_available_models", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "get_available_models", map[string]any{
				"models": rpcModelList(models),
			}))

		// ── set_thinking_level ─────────────────────────────────────────
		case "set_thinking_level":
			cmd, ok := decodeRPC[RPCSetThinkingLevelCommand](env, writeRPC)
			if !ok {
				continue
			}
			if err := sess.SetThinkingLevel(ai.ThinkingLevel(cmd.Level)); err != nil {
				writeRPC(rpcError(env.ID, "set_thinking_level", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "set_thinking_level", nil))

		// ── cycle_thinking_level ─────────────────────────────────────────────
		case "cycle_thinking_level":
			levels := sess.AvailableThinkingLevels()
			if len(levels) <= 1 {
				writeRPC(rpcSuccessNull(env.ID, "cycle_thinking_level"))
				continue
			}
			current := sess.ThinkingLevel()
			currentIndex := max(slices.Index(levels, current), 0)
			nextLevel := levels[(currentIndex+1)%len(levels)]
			if err := sess.SetThinkingLevel(nextLevel); err != nil {
				writeRPC(rpcError(env.ID, "cycle_thinking_level", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "cycle_thinking_level", map[string]string{
				"level": string(nextLevel),
			}))

		case "get_available_thinking_levels":
			levels := sess.AvailableThinkingLevels()
			writeRPC(rpcSuccess(env.ID, "get_available_thinking_levels", map[string]any{"levels": levels}))

		// ── set_auto_retry ────────────────────────────────────────────────
		case "set_auto_retry":
			cmd, ok := decodeRPC[RPCSetAutoRetryCommand](env, writeRPC)
			if !ok {
				continue
			}
			if err := sess.SetAutoRetryEnabled(cmd.Enabled); err != nil {
				writeRPC(rpcError(env.ID, "set_auto_retry", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "set_auto_retry", nil))

		case "abort_retry":
			sess.AbortRetry()
			writeRPC(rpcSuccess(env.ID, "abort_retry", nil))

		// ── steer ────────────────────────────────────────────────────────────
		case "steer":
			cmd, ok := decodeRPC[RPCSteerCommand](env, writeRPC)
			if !ok {
				continue
			}
			if name, _, ok := commandCatalog.builtinCommand(cmd.Message); ok {
				writeRPC(rpcError(env.ID, "steer", fmt.Sprintf("Command %q cannot be queued. Use prompt() or execute the command when not streaming.", name)))
				continue
			}
			sess.Steer(commandCatalog.expandPrompt(cmd.Message), rpcImages(cmd.Images))
			if err := sess.FlushEvents(ctx); err != nil {
				writeRPC(rpcError(env.ID, "steer", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "steer", nil))

		// ── follow_up ─────────────────────────────────────────────────────────
		case "follow_up":
			cmd, ok := decodeRPC[RPCFollowUpCommand](env, writeRPC)
			if !ok {
				continue
			}
			if name, _, ok := commandCatalog.builtinCommand(cmd.Message); ok {
				writeRPC(rpcError(env.ID, "follow_up", fmt.Sprintf("Command %q cannot be queued. Use prompt() or execute the command when not streaming.", name)))
				continue
			}
			sess.FollowUp(commandCatalog.expandPrompt(cmd.Message), rpcImages(cmd.Images))
			if err := sess.FlushEvents(ctx); err != nil {
				writeRPC(rpcError(env.ID, "follow_up", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "follow_up", nil))

		// ── bash ────────────────────────────────────────────────────────────────
		case "bash":
			cmd, ok := decodeRPC[RPCBashCommand](env, writeRPC)
			if !ok {
				continue
			}
			// Run bash in a goroutine (may block for a long time).
			bashWG.Add(1)
			go func(id, command string, excludeFromContext bool) {
				defer bashWG.Done()
				result, err := sess.ExecuteBashWithUpdates(ctx, command, excludeFromContext, func(delta string) {
					writeRPC(RPCBashExecutionUpdate{Type: "bash_execution_update", ID: id, Delta: delta})
				})
				if err != nil {
					writeRPC(rpcError(id, "bash", err.Error()))
					return
				}
				writeRPC(rpcSuccess(id, "bash", RPCBashResult{
					Output:         result.Output,
					ExitCode:       result.ExitCode,
					Cancelled:      result.Cancelled,
					Truncated:      result.Truncated,
					FullOutputPath: result.FullOutputPath,
				}))
			}(env.ID, cmd.Command, cmd.ExcludeFromContext)

		// ── abort_bash ────────────────────────────────────────────────────────
		case "abort_bash":
			sess.AbortBash()
			writeRPC(rpcSuccess(env.ID, "abort_bash", nil))

		// ── set_session_name ─────────────────────────────────────────────────
		case "set_session_name":
			cmd, ok := decodeRPC[RPCSetSessionNameCommand](env, writeRPC)
			if !ok {
				continue
			}
			name := strings.TrimSpace(cmd.Name)
			if name == "" {
				writeRPC(rpcError(env.ID, "set_session_name", "Session name cannot be empty"))
				continue
			}
			if err := sess.SetSessionName(name); err != nil {
				writeRPC(rpcError(env.ID, "set_session_name", err.Error()))
				continue
			}
			name = sess.SessionName()
			writeRPC(rpcSessionInfoChanged(name))
			writeRPC(rpcSuccess(env.ID, "set_session_name", nil))

		// ── get_session_stats ────────────────────────────────────────────────
		case "get_session_stats":
			writeRPC(rpcSuccess(env.ID, "get_session_stats", sess.GetSessionStats()))

		case "export_html":
			cmd, ok := decodeRPC[RPCExportHTMLCommand](env, writeRPC)
			if !ok {
				continue
			}
			if sess.Path() == "" {
				writeRPC(rpcError(env.ID, "export_html", "Cannot export an in-memory session"))
				continue
			}
			outputPath, err := codingexport.ExportFromFile(sess.Path(), cmd.OutputPath)
			if err != nil {
				writeRPC(rpcError(env.ID, "export_html", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "export_html", map[string]string{"path": outputPath}))

		case "switch_session":
			cmd, ok := decodeRPC[RPCSwitchSessionCommand](env, writeRPC)
			if !ok {
				continue
			}
			if cmd.SessionPath == "" {
				writeRPC(rpcError(env.ID, "switch_session", "sessionPath is required"))
				continue
			}
			next, err := newSessionManagerWithDir(cwd, sessionDir).Load(cmd.SessionPath)
			if err != nil {
				writeRPC(rpcError(env.ID, "switch_session", err.Error()))
				continue
			}
			settleSessionWork()
			sess.ReplaceInner(next)
			writeRPC(rpcSuccess(env.ID, "switch_session", RPCCancelledResult{Cancelled: false}))

		case "fork":
			cmd, ok := decodeRPC[RPCForkCommand](env, writeRPC)
			if !ok {
				continue
			}
			if cmd.EntryID == "" {
				writeRPC(rpcError(env.ID, "fork", "Invalid entry ID for forking"))
				continue
			}
			forked, text, err := newSessionManagerWithDir(cwd, sessionDir).ForkToNewSession(sess.Inner(), cmd.EntryID)
			if err != nil {
				writeRPC(rpcError(env.ID, "fork", err.Error()))
				continue
			}
			settleSessionWork()
			sess.ReplaceInner(forked)
			writeRPC(rpcSuccess(env.ID, "fork", RPCForkResult{Text: text, Cancelled: false}))

		case "clone":
			leaf := sess.LeafID()
			if leaf == nil {
				writeRPC(rpcError(env.ID, "clone", "Cannot clone session: no current entry selected"))
				continue
			}
			cloned, err := newSessionManagerWithDir(cwd, sessionDir).Clone(sess.Inner(), *leaf)
			if err != nil {
				writeRPC(rpcError(env.ID, "clone", err.Error()))
				continue
			}
			settleSessionWork()
			sess.ReplaceInner(cloned)
			writeRPC(rpcSuccess(env.ID, "clone", RPCCancelledResult{Cancelled: false}))

		// ── get_messages ────────────────────────────────────────────────────────
		case "get_messages":
			writeRPC(rpcSuccess(env.ID, "get_messages", map[string]any{
				"messages": sess.Messages(),
			}))

		// ── get_last_assistant_text ───────────────────────────────────────────
		case "get_last_assistant_text":
			// With no assistant text the data is {}: the key is omitted when nil.
			if text := sess.LastAssistantText(); text != nil {
				writeRPC(rpcSuccess(env.ID, "get_last_assistant_text", map[string]any{
					"text": *text,
				}))
			} else {
				writeRPC(rpcSuccess(env.ID, "get_last_assistant_text", map[string]any{}))
			}

		// ── get_fork_messages ─────────────────────────────────────────────────
		case "get_fork_messages":
			writeRPC(rpcSuccess(env.ID, "get_fork_messages", map[string]any{
				"messages": sess.UserMessagesForForking(),
			}))

		// ── get_entries ───────────────────────────────────────────────────────
		case "get_entries":
			cmd, ok := decodeRPC[RPCGetEntriesCommand](env, writeRPC)
			if !ok {
				continue
			}
			entries, err := rpcEntriesSince(sess.Entries(), cmd.Since)
			if err != nil {
				writeRPC(rpcError(env.ID, "get_entries", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "get_entries", rpcEntriesData{
				Entries: entries,
				LeafID:  sess.LeafID(),
			}))

		// ── get_tree ──────────────────────────────────────────────────────────
		case "get_tree":
			writeRPC(rpcSuccess(env.ID, "get_tree", rpcTreeData{
				Tree:   rpcTree(sess.Tree()),
				LeafID: sess.LeafID(),
			}))

		// ── set_steering_mode / set_follow_up_mode ─────────────────────────
		// Persist to settings so the value survives sessions.
		case "set_steering_mode":
			cmd, ok := decodeRPC[RPCSetSteeringModeCommand](env, writeRPC)
			if !ok {
				continue
			}
			if err := sess.SetSteeringMode(agent.QueueMode(cmd.Mode)); err != nil {
				writeRPC(rpcError(env.ID, "set_steering_mode", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "set_steering_mode", nil))

		case "set_follow_up_mode":
			cmd, ok := decodeRPC[RPCSetFollowUpModeCommand](env, writeRPC)
			if !ok {
				continue
			}
			if err := sess.SetFollowUpMode(agent.QueueMode(cmd.Mode)); err != nil {
				writeRPC(rpcError(env.ID, "set_follow_up_mode", err.Error()))
				continue
			}
			writeRPC(rpcSuccess(env.ID, "set_follow_up_mode", nil))

		default:
			// Echo the request id so clients can correlate the error response.
			writeRPC(rpcError(env.ID, env.Type,
				fmt.Sprintf("Unknown command: %s", env.Type)))
		} // end switch
	} // end for scanner.Scan()

	if scanErr := <-inputDone; scanErr != nil && !errors.Is(scanErr, io.EOF) {
		writeRPC(RPCErrorEvent{
			Type:    "error",
			Message: fmt.Sprintf("stdin read error: %v", scanErr),
		})
	}

	cancel()
	settleSessionWork()

	// Session Close drains and closes the events channel.
	_ = sess.Close()
	<-eventsDone
	select {
	case <-eventConversionErr:
		return 1
	default:
	}

	return 0
}

type rpcEntriesData struct {
	Entries []codingagent.SessionEntry `json:"entries"`
	LeafID  *string                    `json:"leafId"`
}

type rpcTreeData struct {
	Tree   []rpcSessionTreeNode `json:"tree"`
	LeafID *string              `json:"leafId"`
}

// rpcSessionTreeNode is the JSON shape returned by get_tree.
// The internal tree uses exported Go fields and a synthetic root; RPC returns
// lower-case keys and only real root entries.
type rpcSessionTreeNode struct {
	Entry          codingagent.SessionEntry `json:"entry"`
	Children       []rpcSessionTreeNode     `json:"children"`
	Label          string                   `json:"label,omitempty"`
	LabelTimestamp string                   `json:"labelTimestamp,omitempty"`
}

func rpcAvailableModels(services *coding.Services, current *ai.Model) ([]*ai.Model, error) {
	registry := services.Registry()
	entries := registry.GetAvailable()
	seen := make(map[string]struct{}, len(entries))
	models := make([]*ai.Model, 0, len(entries))
	for _, entry := range entries {
		model, err := coding.BuildModel(entry.ProviderID+"/"+entry.ModelID, services)
		if err != nil {
			return nil, err
		}
		key := entry.ProviderID + "\x00" + entry.ModelID
		seen[key] = struct{}{}
		models = append(models, model)
	}

	authed := codingagent.AuthenticatedProviders(services.AgentDir())
	for _, generated := range ai.ListModels("") {
		if !authed[generated.Provider] {
			continue
		}
		key := generated.Provider + "\x00" + generated.ID
		if _, exists := seen[key]; exists {
			continue
		}
		model, err := coding.BuildModel(generated.Provider+"/"+generated.ID, services)
		if err != nil {
			return nil, err
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	if current != nil {
		provider := current.ProviderMeta.ProviderID
		if provider == "" && current.Provider != nil {
			provider = current.Provider.ID()
		}
		key := provider + "\x00" + current.ID
		if _, exists := seen[key]; !exists {
			models = append(models, current)
		}
	}
	return models, nil
}

func rpcModelList(models []*ai.Model) []*RPCModel {
	out := make([]*RPCModel, len(models))
	for i, model := range models {
		out[i] = rpcModelValue(model)
	}
	return out
}

func rpcEntriesSince(entries []codingagent.SessionEntry, since string) ([]codingagent.SessionEntry, error) {
	if since == "" {
		return entries, nil
	}
	for i, entry := range entries {
		if entry.Base.ID == since {
			return entries[i+1:], nil
		}
	}
	return nil, fmt.Errorf("Entry not found: %s", since)
}

func rpcTree(root *codingagent.SessionTreeNode) []rpcSessionTreeNode {
	if root == nil {
		return nil
	}
	out := make([]rpcSessionTreeNode, len(root.Children))
	for i, child := range root.Children {
		out[i] = rpcTreeNode(child)
	}
	return out
}

func rpcTreeNode(node *codingagent.SessionTreeNode) rpcSessionTreeNode {
	out := rpcSessionTreeNode{
		Entry:          node.Entry,
		Children:       make([]rpcSessionTreeNode, len(node.Children)),
		Label:          node.Label,
		LabelTimestamp: node.LabelTimestamp,
	}
	for i, child := range node.Children {
		out.Children[i] = rpcTreeNode(child)
	}
	return out
}

// readJSONLLines reads strict JSONL records from r. It splits only on LF,
// removes one trailing CR, delivers blank records, and delivers a final
// unterminated record. Returning false from onLine stops the read.
func readJSONLLines(r io.Reader, onLine func([]byte) bool) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			if !onLine(line) {
				return nil
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// decodeRPC unmarshals a command's payload, writing an error response for the
// command when it fails.
func decodeRPC[T any](env RPCCommandEnvelope, writeRPC func(any)) (T, bool) {
	var cmd T
	if err := json.Unmarshal(env.Raw, &cmd); err != nil {
		writeRPC(rpcError(env.ID, env.Type, err.Error()))
		return cmd, false
	}
	return cmd, true
}
