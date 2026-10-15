// Headless JSONL command and event loop.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/coding/host"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/export"
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
func (c rpcCommandCatalog) executeCommand(ctx context.Context, name, args string) bool {
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

	// Build RPCOptions for ServeRPC.
	opts := host.RPCOptions{
		In:    os.Stdin,
		Write: writeRPC,
		Cmds: host.RPCCommands{
			Commands: commandCatalog.commands,
			Resolve:  commandCatalog.builtinCommand,
			Expand:   commandCatalog.expandPrompt,
			Run: func(ctx context.Context, name, args string) {
				commandCatalog.executeCommand(ctx, name, args)
			},
			Export: export.ExportFromFile,
		},
		CWD:        cwd,
		SessionDir: sessionDir,
		NoSession:  flags.NoSession,
	}

	return host.ServeRPC(ctx, services, sess, opts)
}
