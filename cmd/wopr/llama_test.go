package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/alexrudloff/wopr/coding"
)

// RPC lists and runs the built-in /llama command: first in get_commands, and
// in RPC mode it only warns.
func TestRPCCatalogListsAndRunsBuiltInLlamaCommand(t *testing.T) {
	t.Setenv("LLAMA_BASE_URL", "")
	t.Setenv("LLAMA_API_KEY", "")
	dir := t.TempDir()
	services, err := coding.NewServices(coding.ServicesOptions{CWD: dir, AgentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	host := startBuiltInLlama(context.Background(), services)
	var notices [][2]string
	catalog := rpcCommandCatalog{llama: host, notify: func(message, kind string) {
		notices = append(notices, [2]string{message, kind})
	}}

	commands := catalog.commands()
	want := RPCSlashCommand{
		Name: "llama", Description: "Manage llama.cpp router models", Source: "builtin",
		SourceInfo: RPCSourceInfo{Path: "<inline:llama.cpp>", Source: "inline", Scope: "temporary", Origin: "top-level"},
	}
	if len(commands) != 1 || !reflect.DeepEqual(commands[0], want) {
		t.Fatalf("commands = %#v", commands)
	}
	if expanded, handled := catalog.routePrompt(context.Background(), "/llama now"); !handled || expanded != "" {
		t.Fatalf("routePrompt(/llama) = %q, %v", expanded, handled)
	}
	if want := [][2]string{{"/llama is available in interactive mode", "warning"}}; !reflect.DeepEqual(notices, want) {
		t.Fatalf("notices = %v", notices)
	}
}
