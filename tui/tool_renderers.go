package tui

import "slices"

// tool_renderers.go: the built-in tool renderer registry.
//
// Built-in renderers are keyed by tool name and apply to any registered
// definition that does not supply its own: FormatBuiltinToolHeader renders
// the call and internal/codingagent toolBodyRenderer the result.

// builtInToolRendererNames lists the tools with built-in renderers.
var builtInToolRendererNames = [...]string{"read", "bash", "powershell", "edit", "write", "grep", "find", "ls"}

// HasBuiltInToolRenderers reports whether toolName has built-in renderers.
// They are the fallback for a registered definition (for example an extension
// override of a built-in name) that supplies no call or result renderer of
// its own.
func HasBuiltInToolRenderers(toolName string) bool {
	return slices.Contains(builtInToolRendererNames[:], toolName)
}

// ShellToolPrompt returns the prompt a built-in shell tool renders with: "$"
// for bash and "PS>" for powershell. ok is false for other tools.
func ShellToolPrompt(toolName string) (prompt string, ok bool) {
	switch toolName {
	case "bash":
		return "$", true
	case "powershell":
		return "PS>", true
	}
	return "", false
}

// IsShellTool reports whether toolName uses the shared shell renderers.
func IsShellTool(toolName string) bool {
	_, ok := ShellToolPrompt(toolName)
	return ok
}
