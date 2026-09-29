package tools

import "encoding/json"

// ResolvePath resolves a path the way the file tools do: relative to cwd,
// with ~ expanded.
func ResolvePath(cwd, path string) string { return resolvePath(cwd, path) }

// ChangedPaths returns the absolute paths a write, edit, or apply_patch call
// would change, resolved as the tool resolves them; nil for any other tool.
func ChangedPaths(cwd, toolName string, args json.RawMessage) []string {
	switch toolName {
	case "write", "edit":
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &in) != nil || in.Path == "" {
			return nil
		}
		return []string{resolvePath(cwd, in.Path)}
	case ApplyPatchName:
		var in struct {
			Input string `json:"input"`
		}
		if json.Unmarshal(args, &in) != nil {
			return nil
		}
		ops, err := ParsePatch(in.Input)
		if err != nil {
			return nil
		}
		var out []string
		for _, op := range ops {
			out = append(out, resolvePath(cwd, op.path))
			if op.moveTo != "" {
				out = append(out, resolvePath(cwd, op.moveTo))
			}
		}
		return out
	}
	return nil
}
