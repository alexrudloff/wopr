package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// RPCOptions is what a caller supplies to ServeRPC. Every field is required.
type RPCOptions struct {
	// In is the JSONL command stream. The engine never names a stream of its
	// own, so one process can drive one session per call from any reader.
	In io.Reader
	// Write is the engine's single write seam: every response and event goes
	// through it. Nil panics on the first write.
	Write func(any)
	// Cmds is the command catalog. The engine cannot build these itself — the
	// catalog's wire shapes are protocol types assembled in package main, which
	// this package cannot name.
	Cmds RPCCommands
	// CWD and SessionDir locate the session transcript directory; NoSession is
	// the CLI's --no-session.
	CWD        string
	SessionDir string
	NoSession  bool
}

// RPCCommands is the catalog dependency ServeRPC calls.
type RPCCommands struct {
	// Commands returns the get_commands list, whose shape the catalog owns.
	Commands func() []RPCSlashCommand
	// Resolve returns the built-in command a message invokes, if any.
	Resolve func(message string) (name, args string, ok bool)
	// Expand substitutes a skill or prompt-template invocation.
	Expand func(message string) string
	// Run executes a resolved built-in command. Its bool return is dropped:
	// the command's own output is the response, and there is none to send.
	Run func(ctx context.Context, name, args string)
	// Export writes an HTML export of a session transcript and returns its path.
	Export func(sessionPath, outputPath string) (string, error)
}

// RPCSlashCommand is the wire shape for a slash command.
type RPCSlashCommand struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Source      string        `json:"source"`
	SourceInfo  RPCSourceInfo `json:"sourceInfo"`
}

// RPCSourceInfo carries source metadata for a slash command.
type RPCSourceInfo struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Origin  string `json:"origin"`
	BaseDir string `json:"baseDir,omitempty"`
}

// ─── Commands (stdin) ─────────────────────────────────────────────────────────

// RPCCommandEnvelope is the raw JSON wrapper. The Type field selects which
// concrete command struct to unmarshal into.
type RPCCommandEnvelope struct {
	ID   string          `json:"id,omitempty"`
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"` // full original bytes, set by parseRPCCommand
}

// RPCImageContent is one image part of a prompt or a tool result.
type RPCImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

func (i RPCImageContent) imageContent() ai.ImageContent {
	return ai.ImageContent{Data: i.Data, MimeType: i.MimeType}
}

// RPCPromptCommand sends a message to the agent.
type RPCPromptCommand struct {
	ID                string            `json:"id,omitempty"`
	Type              string            `json:"type"`
	Message           string            `json:"message"`
	Images            []RPCImageContent `json:"images,omitempty"`
	StreamingBehavior string            `json:"streamingBehavior,omitempty"`
}

type RPCNewSessionCommand struct {
	ID            string `json:"id,omitempty"`
	Type          string `json:"type"`
	ParentSession string `json:"parentSession,omitempty"`
}

type RPCExportHTMLCommand struct {
	ID         string `json:"id,omitempty"`
	Type       string `json:"type"`
	OutputPath string `json:"outputPath,omitempty"`
}

type RPCSwitchSessionCommand struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	SessionPath string `json:"sessionPath"`
}

type RPCForkCommand struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	EntryID string `json:"entryId"`
}

// RPCSetModelCommand changes the active model.
type RPCSetModelCommand struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// RPCCompactCommand triggers manual compaction.
type RPCCompactCommand struct {
	ID                 string `json:"id,omitempty"`
	Type               string `json:"type"`
	CustomInstructions string `json:"customInstructions,omitempty"`
}

// RPCSetAutoCompactionCommand enables/disables auto-compaction.
type RPCSetAutoCompactionCommand struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// RPCSetThinkingLevelCommand changes the thinking level.
type RPCSetThinkingLevelCommand struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Level string `json:"level"` // "off"|"minimal"|"low"|"medium"|"high"|"xhigh"|"max"
}

// ─── Responses / Events (stdout) ─────────────────────────────────────────────

// RPCResponse wraps a command acknowledgement.
type RPCResponse struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`    // always "response"
	Command string `json:"command"` // mirrors the command type
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

type rpcNullResponse struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Data    any    `json:"data"`
}

type RPCClearQueueData struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

type RPCCancelledResult struct {
	Cancelled bool `json:"cancelled"`
}

type RPCForkResult struct {
	Text      string `json:"text"`
	Cancelled bool   `json:"cancelled"`
}

type RPCModelCycleResult struct {
	Model         *RPCModel        `json:"model"`
	ThinkingLevel ai.ThinkingLevel `json:"thinkingLevel"`
	// IsScoped is always false: model scoping was removed; the field stays
	// for RPC clients that read it.
	IsScoped bool `json:"isScoped"`
}

type RPCGetCommandsData struct {
	Commands []RPCSlashCommand `json:"commands"`
}

type RPCModelCostTier struct {
	InputTokensAbove int     `json:"inputTokensAbove"`
	Input            float64 `json:"input"`
	Output           float64 `json:"output"`
	CacheRead        float64 `json:"cacheRead"`
	CacheWrite       float64 `json:"cacheWrite"`
}

type RPCModelCost struct {
	Input      float64            `json:"input"`
	Output     float64            `json:"output"`
	CacheRead  float64            `json:"cacheRead"`
	CacheWrite float64            `json:"cacheWrite"`
	Tiers      []RPCModelCostTier `json:"tiers,omitempty"`
}

type RPCModel struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	API              ai.API               `json:"api"`
	Provider         string               `json:"provider"`
	BaseURL          string               `json:"baseUrl"`
	Reasoning        bool                 `json:"reasoning"`
	ThinkingLevelMap ai.ThinkingLevelMap  `json:"thinkingLevelMap,omitempty"`
	Input            []string             `json:"input"`
	Cost             RPCModelCost         `json:"cost"`
	PromptCache      ai.ModelPromptCache  `json:"promptCache,omitempty"`
	ContextWindow    int                  `json:"contextWindow"`
	MaxTokens        int                  `json:"maxTokens"`
	SamplingParams   map[string]any       `json:"samplingParams,omitempty"`
	Headers          map[string]string    `json:"headers,omitempty"`
	Compat           *ai.ModelCompat      `json:"compat,omitempty"`
	InputLimits      *ai.ModelInputLimits `json:"inputLimits,omitempty"`
}

// RPCSessionState is the payload for a get_state response.
type RPCSessionState struct {
	Model                 *RPCModel `json:"model"`
	ThinkingLevel         string    `json:"thinkingLevel"`
	IsStreaming           bool      `json:"isStreaming"`
	IsCompacting          bool      `json:"isCompacting"`
	SteeringMode          string    `json:"steeringMode"`
	FollowUpMode          string    `json:"followUpMode"`
	SessionFile           string    `json:"sessionFile,omitempty"`
	SessionID             string    `json:"sessionId"`
	SessionName           string    `json:"sessionName,omitempty"`
	AutoCompactionEnabled bool      `json:"autoCompactionEnabled"`
	MessageCount          int       `json:"messageCount"`
	PendingMessageCount   int       `json:"pendingMessageCount"`
}

type RPCQueueUpdateEvent struct {
	Type     string   `json:"type"`
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

type RPCThinkingLevelChangedEvent struct {
	Type  string           `json:"type"`
	Level ai.ThinkingLevel `json:"level"`
}

// RPCErrorEvent signals an error that is not tied to a specific command.
type RPCErrorEvent struct {
	Type    string `json:"type"` // "error"
	Message string `json:"message"`
}

// ─── Additional commands ────────────────────────────────────────────────

// RPCSteerCommand queues a steering message for the active agent turn.
type RPCSteerCommand struct {
	ID      string            `json:"id,omitempty"`
	Type    string            `json:"type"`
	Message string            `json:"message"`
	Images  []RPCImageContent `json:"images,omitempty"`
}

// RPCFollowUpCommand queues a message after the active turn settles.
type RPCFollowUpCommand struct {
	ID      string            `json:"id,omitempty"`
	Type    string            `json:"type"`
	Message string            `json:"message"`
	Images  []RPCImageContent `json:"images,omitempty"`
}

// RPCBashCommand executes a bash command outside the LLM agent loop.
type RPCBashCommand struct {
	ID                 string `json:"id,omitempty"`
	Type               string `json:"type"`
	Command            string `json:"command"`
	ExcludeFromContext bool   `json:"excludeFromContext,omitempty"`
}

// RPCSetAutoRetryCommand enables/disables auto-retry.
type RPCSetAutoRetryCommand struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// RPCSetSessionNameCommand sets the current session name.
type RPCSetSessionNameCommand struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// RPCGetEntriesCommand returns session entries, optionally after the supplied id.
type RPCGetEntriesCommand struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Since string `json:"since,omitempty"`
}

// RPCSetSteeringModeCommand sets the steering queue mode.
type RPCSetSteeringModeCommand struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

// RPCSetFollowUpModeCommand sets the follow-up queue mode.
type RPCSetFollowUpModeCommand struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

// RPCBashResult is the response payload for the bash command.
// Mirrors coding.BashResult.
type RPCBashResult struct {
	Output         string `json:"output"`
	ExitCode       int    `json:"exitCode"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

type RPCBashExecutionUpdate struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Delta string `json:"delta"`
}

type RPCCompactionResult struct {
	Summary              string         `json:"summary"`
	FirstKeptEntryID     string         `json:"firstKeptEntryId"`
	TokensBefore         int            `json:"tokensBefore"`
	EstimatedTokensAfter int            `json:"estimatedTokensAfter"`
	Usage                map[string]any `json:"usage,omitempty"`
	Details              any            `json:"details,omitempty"`
}

func rpcCompactionResult(result *coding.CompactionResult) RPCCompactionResult {
	var usage map[string]any
	if result.Usage != nil {
		usage = rpcUsage(result.Usage)
	}
	return RPCCompactionResult{
		Summary:              result.Summary,
		FirstKeptEntryID:     result.FirstKeptEntryID,
		TokensBefore:         result.TokensBefore,
		EstimatedTokensAfter: result.EstimatedTokensAfter,
		Usage:                usage,
		Details:              rpcOptionalCompactionDetails(result.Details),
	}
}

func rpcOptionalCompactionDetails(details any) any {
	if value, ok := details.(compaction.CompactionDetails); ok && value.ReadFiles == nil && value.ModifiedFiles == nil {
		return nil
	}
	return details
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func rpcImages(images []RPCImageContent) []ai.ImageContent {
	out := make([]ai.ImageContent, len(images))
	for i, image := range images {
		out[i] = image.imageContent()
	}
	return out
}

// unmarshalRPC is json.Unmarshal with this package's name rewritten back to
// "main" in a type error. encoding/json embeds <pkg>.<Type> in its message, and
// these wire types moved out of package main, so without the rewrite every
// malformed-command error would change on the wire.
//
// ponytail: the rewrite keeps bytes, not accuracy — the type really lives here.
// Drop it when no client matches the text.
func unmarshalRPC(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return errors.New(strings.ReplaceAll(err.Error(), "host.", "main."))
	}
	return err
}

// parseRPCCommand parses a JSON line from stdin into a typed command.
// Returns the envelope (with Type and ID) and any parse error.
func parseRPCCommand(line []byte) (RPCCommandEnvelope, error) {
	var env RPCCommandEnvelope
	if err := unmarshalRPC(line, &env); err != nil {
		return RPCCommandEnvelope{}, err
	}
	env.Raw = append([]byte(nil), line...) // safe copy
	return env, nil
}

// rpcSuccess builds a success response for a command.
func rpcSuccess(id, command string, data any) RPCResponse {
	return RPCResponse{ID: id, Type: "response", Command: command, Success: true, Data: data}
}

func rpcSuccessNull(id, command string) rpcNullResponse {
	return rpcNullResponse{ID: id, Type: "response", Command: command, Success: true, Data: nil}
}

// rpcError builds an error response for a command.
func rpcError(id, command, message string) RPCResponse {
	return RPCResponse{ID: id, Type: "response", Command: command, Success: false, Error: message}
}

func rpcModelValue(model *ai.Model) *RPCModel {
	if model == nil {
		return &RPCModel{
			ID:       "unknown",
			Name:     "unknown",
			API:      ai.API("unknown"),
			Provider: "unknown",
			Input:    []string{},
		}
	}
	provider := ModelProviderID(model)
	input := []string{"text"}
	if model.Capabilities.SupportsImages {
		input = append(input, "image")
	}
	cost := RPCModelCost{
		Input:      model.Capabilities.InputCostPer1M,
		Output:     model.Capabilities.OutputCostPer1M,
		CacheRead:  model.Capabilities.CacheReadCostPer1M,
		CacheWrite: model.Capabilities.CacheWriteCostPer1M,
	}
	if len(model.Capabilities.CostTiers) > 0 {
		cost.Tiers = make([]RPCModelCostTier, len(model.Capabilities.CostTiers))
		for i, tier := range model.Capabilities.CostTiers {
			cost.Tiers[i] = RPCModelCostTier{
				InputTokensAbove: tier.InputTokensAbove,
				Input:            tier.InputCostPer1M,
				Output:           tier.OutputCostPer1M,
				CacheRead:        tier.CacheReadCostPer1M,
				CacheWrite:       tier.CacheWriteCostPer1M,
			}
		}
	}
	return &RPCModel{
		ID:               model.ID,
		Name:             model.DisplayName,
		API:              model.ProviderMeta.API,
		Provider:         provider,
		BaseURL:          model.ProviderMeta.BaseURL,
		Reasoning:        model.ProviderMeta.Reasoning || model.Capabilities.MaxThinking != "",
		ThinkingLevelMap: model.ThinkingLevelMap,
		Input:            input,
		Cost:             cost,
		PromptCache:      model.PromptCache,
		ContextWindow:    model.Capabilities.ContextWindow,
		MaxTokens:        model.Capabilities.MaxOutputTokens,
		SamplingParams:   model.SamplingParams,
		Headers:          model.ProviderMeta.Headers,
		Compat:           model.ProviderMeta.Compat,
		InputLimits:      model.InputLimits.Clone(),
	}
}

// RPCSessionInfoChangedEvent reports the effective Session name. Name is
// omitted when it is empty.
type RPCSessionInfoChangedEvent struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// rpcSessionInfoChanged constructs the session_info_changed event.
func rpcSessionInfoChanged(name string) RPCSessionInfoChangedEvent {
	return RPCSessionInfoChangedEvent{Type: "session_info_changed", Name: name}
}

// ServeRPC is the headless JSONL command and event loop. It reads commands
// from opts.In, writes responses and events through opts.Write, and uses
// opts.Cmds for command resolution. Returns 0 on clean shutdown, 1 on fatal error.
func ServeRPC(ctx context.Context, svcs *coding.Services, sess *coding.Session, opts RPCOptions) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	writeRPC := opts.Write

	cwd, sessionDir := opts.CWD, opts.SessionDir

	// ── Event forwarder ────────────────────────────────────────────────────
	// Forward AgentSessionEvent JSON objects: RPC mode subscribes to session
	// events and writes each event as a JSONL object; WireAgentEvent adapts
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
		models, err := AvailableModels(svcs, currentSession.Model())
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
			converted, err := WireAgentEvent(ev)
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
		err := readJSONLLines(opts.In, func(line []byte) bool {
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
			if name, args, ok := opts.Cmds.Resolve(cmd.Message); ok {
				go func(id, commandName, commandArgs string) {
					opts.Cmds.Run(ctx, commandName, commandArgs)
					writeRPC(rpcSuccess(id, "prompt", nil))
				}(env.ID, name, args)
				continue
			}
			if sess.IsCompacting() {
				writeRPC(rpcError(env.ID, "prompt", "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry."))
				continue
			}
			images := rpcImages(cmd.Images)
			cmd.Message = opts.Cmds.Expand(cmd.Message)
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
				providerID := ModelProviderID(model)
				if providerID != "test-faux" && !svcs.Registry().HasConfiguredAuth(providerID) {
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
				_, sendErr := sess.SendContentWithPreflight(ctx, coding.BuildUserContent(cmd.Message, images), "", func() {
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
			if opts.NoSession {
				next = codingagent.NewSession(sessionID, cwd)
			} else {
				next, err = newManager(cwd, sessionDir).Create(sessionID, cmd.ParentSession)
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
			writeRPC(rpcSuccess(env.ID, "get_commands", RPCGetCommandsData{Commands: opts.Cmds.Commands()}))

		// ── get_state ────────────────────────────────────────────────────
		case "get_state":
			compactionEnabled := svcs.SettingsManager().GetCompactionSettings().Enabled
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
			models, err := AvailableModels(svcs, sess.Model())
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
			if err := svcs.SettingsManager().UpdateGlobal(func(s *codingagent.Settings) {
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
			models, err := AvailableModels(svcs, sess.Model())
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
			if name, _, ok := opts.Cmds.Resolve(cmd.Message); ok {
				writeRPC(rpcError(env.ID, "steer", fmt.Sprintf("Command %q cannot be queued. Use prompt() or execute the command when not streaming.", name)))
				continue
			}
			sess.Steer(opts.Cmds.Expand(cmd.Message), rpcImages(cmd.Images))
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
			if name, _, ok := opts.Cmds.Resolve(cmd.Message); ok {
				writeRPC(rpcError(env.ID, "follow_up", fmt.Sprintf("Command %q cannot be queued. Use prompt() or execute the command when not streaming.", name)))
				continue
			}
			sess.FollowUp(opts.Cmds.Expand(cmd.Message), rpcImages(cmd.Images))
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
			outputPath, err := opts.Cmds.Export(sess.Path(), cmd.OutputPath)
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
			next, err := newManager(cwd, sessionDir).Load(cmd.SessionPath)
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
			forked, text, err := newManager(cwd, sessionDir).ForkToNewSession(sess.Inner(), cmd.EntryID)
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
			cloned, err := newManager(cwd, sessionDir).Clone(sess.Inner(), *leaf)
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
		provider := ModelProviderID(current)
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
	if err := unmarshalRPC(env.Raw, &cmd); err != nil {
		writeRPC(rpcError(env.ID, env.Type, err.Error()))
		return cmd, false
	}
	return cmd, true
}

func rpcUsage(usage *ai.Usage) map[string]any {
	if usage == nil {
		usage = &ai.Usage{}
	}
	out := map[string]any{
		"input": usage.Input, "output": usage.Output, "cacheRead": usage.CacheRead,
		"cacheWrite": usage.CacheWrite, "totalTokens": usage.TotalTokens,
		"cost": map[string]any{
			"input": usage.Cost.Input, "output": usage.Cost.Output,
			"cacheRead": usage.Cost.CacheRead, "cacheWrite": usage.Cost.CacheWrite,
			"total": usage.Cost.Total,
		},
	}
	if usage.CacheWrite1h != nil {
		out["cacheWrite1h"] = *usage.CacheWrite1h
	}
	if usage.Reasoning != nil {
		out["reasoning"] = *usage.Reasoning
	}
	return out
}

func tryDecodeJSON(text string) (any, bool) {
	if text == "" {
		return nil, false
	}
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}
	return value, true
}

// newManager is a copy of newSessionManagerWithDir from cmd/wopr/main.go,
// duplicated because a main package cannot export a helper.
func newManager(cwd, sessionDir string) *codingagent.SessionManager {
	if sessionDir != "" {
		return codingagent.NewSessionManagerWithDir(cwd, sessionDir)
	}
	return codingagent.NewSessionManager(cwd)
}

// AvailableModels returns all available models for the given services,
// including the current model if provided. This is an exported version of
// rpcAvailableModels for use by serve.
func AvailableModels(services *coding.Services, current *ai.Model) ([]*ai.Model, error) {
	return rpcAvailableModels(services, current)
}

// ModelProviderID returns m's provider identity. ProviderMeta.ProviderID can
// legitimately be empty, which is why the provider's own id is the fallback.
func ModelProviderID(m *ai.Model) string {
	if m.ProviderMeta.ProviderID != "" {
		return m.ProviderMeta.ProviderID
	}
	if m.Provider != nil {
		return m.Provider.ID()
	}
	return ""
}
