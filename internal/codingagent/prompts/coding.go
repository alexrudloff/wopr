// Package prompts builds the default coding-agent system prompt.
//
// The layout is an untagged preamble, then tagged sections (tools, rules,
// docs, addendum, discipline, project_context, skills, cwd). The discipline
// section is wopr's always-on minimal-code rule set; it precedes the
// per-project sections so it stays in the stable prompt prefix.
// BuildDefaultPrompt's callers pass the
// agent session's inputs: selected tools, their prompt snippets and
// guidelines, context files, and skills.
package prompts

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
)

// Skill is the subset of a discovered skill that the prompt lists.
type Skill struct {
	Name        string
	Description string
	Path        string
	// DisableModelInvocation hides the skill from the model (the
	// disable-model-invocation frontmatter).
	DisableModelInvocation bool
}

// Options configures BuildDefaultPrompt.
type Options struct {
	// Cwd is the working directory shown to the model.
	Cwd string
	// Tools are the selected tool names in the order the model sees them.
	Tools []string
	// ToolHints maps a tool name to its prompt snippet. Only tools that have a
	// snippet are listed.
	ToolHints map[string]string
	// ToolGuidelines maps a tool name to its prompt guidelines. Guidelines of
	// selected tools become rules, deduplicated in tool order.
	ToolGuidelines map[string][]string
	// PromptGuidelines are rules that do not belong to one tool.
	PromptGuidelines []string
	// Skills lists discovered skills.
	Skills []Skill
	// CustomPrompt is an agent definition's body. AppendMode decides its role.
	CustomPrompt string
	// AppendSystemPrompt is appended as an addendum after the configured preamble.
	AppendSystemPrompt string
	// ContextFiles are the loaded AGENTS.md and CLAUDE.md files.
	ContextFiles []struct{ Path, Content string }
	// DocsPath is the local documentation bundle synced by woprdocs. Empty
	// uses the standard config-root location.
	DocsPath string
	// AppendMode is "append" (default: CustomPrompt becomes the addendum
	// section) or "replace" (CustomPrompt replaces the preamble, and the tools,
	// rules, and docs sections are omitted).
	AppendMode string
}

const preamble = "You are an expert coding assistant operating inside wopr, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files."

// discipline is wopr's minimal-code rule set, present in every default and
// replaced prompt.
const discipline = `Write the least code that correctly does the job. First understand the problem and trace the real flow, then stop at the first rung that holds:
1. Does it need to exist? If the need is speculative, skip it and say so.
2. Already in this codebase? Reuse it.
3. Standard library? Use it.
4. Platform feature (native input, CSS, DB constraint)? Use it.
5. Installed dependency? Use it; never add one for what a few lines do.
6. One line? One line.
7. Otherwise, the minimum code that works.
- Fix bugs at the root cause, once, where all callers route through.
- No unrequested abstractions, config, or scaffolding for later. Deletion over addition, boring over clever, fewest files.
- Never cut input validation, error handling, security, accessibility, or anything requested. Non-trivial logic keeps the one small test that fails if it breaks.
- Mark a deliberate simplification with a known ceiling with a ` + "`simplify:`" + ` comment naming the ceiling and when to upgrade.`

type section struct{ name, content string }

// BuildDefaultPrompt renders the system prompt.
func BuildDefaultPrompt(o Options) string {
	return ai.GetCurrentSystemPrompt([]ai.Message{ai.SystemMessage{Content: ai.SystemText(""), Sections: BuildSystemPromptSections(o)}})
}

// BuildSystemPromptSections retains the ordered, independently replaceable prompt sections.
func BuildSystemPromptSections(o Options) ai.OrderedSections {
	head := preamble
	var sections []section
	if o.AppendMode == "replace" && o.CustomPrompt != "" {
		head = o.CustomPrompt
	} else {
		var tools []string
		for _, name := range o.Tools {
			if hint := o.ToolHints[name]; hint != "" {
				tools = append(tools, "- "+name+": "+hint)
			}
		}
		list := "(none)"
		if len(tools) > 0 {
			list = strings.Join(tools, "\n")
		}
		sections = append(sections,
			section{"tools", list + "\n\nIn addition to the tools above, you may have access to other custom tools depending on the project."},
			section{"rules", renderRules(o.Tools, o.ToolGuidelines, o.PromptGuidelines)},
			section{"docs", docsSection(o.DocsPath)},
		)
		if addendum := strings.TrimSpace(o.CustomPrompt); addendum != "" {
			sections = append(sections, section{"addendum", addendum})
		}
	}
	if o.AppendSystemPrompt != "" {
		if len(sections) > 0 && sections[len(sections)-1].name == "addendum" {
			sections[len(sections)-1].content += "\n\n" + o.AppendSystemPrompt
		} else {
			sections = append(sections, section{"addendum", o.AppendSystemPrompt})
		}
	}
	sections = append(sections, section{"discipline", discipline})
	if len(o.ContextFiles) > 0 {
		parts := []string{"Project-specific instructions and guidelines:"}
		for _, f := range o.ContextFiles {
			parts = append(parts, `<project_instructions path="`+f.Path+`">`+"\n"+f.Content+"\n</project_instructions>")
		}
		sections = append(sections, section{"project_context", strings.Join(parts, "\n\n")})
	}
	if readTool := skillReadTool(o.Tools); readTool != "" {
		if skills := formatSkills(o.Skills, readTool); skills != "" {
			sections = append(sections, section{"skills", skills})
		}
	}
	cwd := cmp.Or(o.Cwd, ".")
	sections = append(sections, section{"cwd", strings.ReplaceAll(cwd, `\`, "/")})
	out := ai.OrderedSections{{Name: "preamble", Value: new(head)}}
	for _, s := range sections {
		out = append(out, ai.PromptSection{Name: s.name, Value: new("<" + s.name + ">\n" + s.content + "\n</" + s.name + ">")})
	}
	return out
}

// docsSection points the model at WOPR's documentation.
func docsSection(root string) string {
	if root == "" {
		root = defaultDocsPath()
	}
	// wopr additive (D22): the docs section points at the materialized WOPR documentation bundle.
	return "WOPR documentation (read only when the user asks about wopr itself, its themes, skills, or TUI):\n" +
		"- Main documentation: " + filepath.Join(root, "index.md") + "\n" +
		"- Additional docs: " + root + "\n" +
		"- When reading wopr docs, resolve docs/... under Additional docs, not the current working directory\n" +
		"- When asked about: themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), environment variables (docs/environment-variables.md)\n" +
		"- When working on wopr topics, read the docs, and follow .md cross-references before implementing\n" +
		"- Always read wopr .md files completely and follow links to related docs (e.g., tui.md for TUI API details)"
}

func defaultDocsPath() string {
	if v := os.Getenv("WOPR_HOME"); v != "" {
		return filepath.Join(v, "docs")
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "wopr", "docs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wopr", "docs")
}

// renderRules renders the guidelines section.
func renderRules(tools []string, toolGuidelines map[string][]string, promptGuidelines []string) string {
	var lines []string
	for _, rule := range guidelinesFor(tools, toolGuidelines, promptGuidelines) {
		lines = append(lines, "- "+rule)
	}
	return strings.Join(lines, "\n")
}

func guidelinesFor(tools []string, toolGuidelines map[string][]string, promptGuidelines []string) []string {
	has := func(name string) bool { return slices.Contains(tools, name) }
	seen := map[string]bool{}
	var out []string
	add := func(rule string) {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			seen[rule] = true
			out = append(out, rule)
		}
	}
	bash, powershell := has("bash"), has("powershell")
	if (bash || powershell) && !has("grep") && !has("find") && !has("ls") {
		switch {
		case bash && powershell:
			add("Use bash or PowerShell for file operations like listing, searching, and finding files")
		case powershell:
			add("Use PowerShell for file operations like listing, searching, and finding files")
		default:
			add("Use bash for file operations like ls, rg, find")
		}
	}
	if bash || powershell {
		add("Check that a tool or interpreter exists before relying on it, and never destroy data the task may need: back up before rewriting git history, deleting work, or opening a database that has -wal/-shm files")
	}
	for _, name := range tools {
		for _, rule := range toolGuidelines[name] {
			add(rule)
		}
	}
	for _, rule := range promptGuidelines {
		add(rule)
	}
	add("Be concise in your responses")
	add("Show file paths clearly when working with files")
	return out
}

func skillReadTool(tools []string) string {
	for _, name := range []string{"read", "bash"} {
		if slices.Contains(tools, name) {
			return name
		}
	}
	return ""
}

// formatSkills renders the skills section, trimmed as the section is.
func formatSkills(skills []Skill, readTool string) string {
	var visible []Skill
	for _, s := range skills {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	load := "Use the read tool to load a skill's file when the task matches its description."
	if readTool != "read" {
		load = "Use bash to load a skill's file when the task matches its description."
	}
	lines := []string{
		"The following skills provide specialized instructions for specific tasks.",
		load,
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	for _, s := range visible {
		lines = append(lines, "  <skill>", "    <name>"+escapeXML(s.Name)+"</name>", "    <description>"+escapeXML(s.Description)+"</description>", "    <location>"+escapeXML(s.Path)+"</location>", "  </skill>")
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func escapeXML(s string) string { return xmlEscaper.Replace(s) }
