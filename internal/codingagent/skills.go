package codingagent

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/coding/resources"
	"github.com/alexrudloff/wopr/internal/codingagent/frontmatter"
)

// SkillDef is a parsed skill: a directory under `<config>/skills/<name>/`
// containing a `SKILL.md` file with frontmatter (name, description) and
// markdown body.
//
// wopr loads these on `--skill <name>` and appends the body
// to the system prompt under a `## Skills` heading.
type SkillDef struct {
	Name                   string
	Description            string
	DisableModelInvocation bool
	// Body is the markdown content (stripped of frontmatter).
	Body string
	// Path is the source SKILL.md file.
	Path string
	// Dir is the skill's containing directory: used so callers can
	// resolve referenced sibling files.
	Dir string
}

// LoadSkill loads a skill by name from `<skillsDir>/<name>/SKILL.md`.
//
// Lookup order (user before project):
//  1. <wopr-config>/skills/<name>/SKILL.md
//  2. <skillsDir>/<name>/SKILL.md  (caller-provided fallback root)
//
// Most callers pass DefaultAgentDir()/skills as skillsDir so the wopr agent tree
// is searched first; we keep the parameter so SDK consumers can point
// at a vendored skills tree without env juggling.
func LoadSkill(skillsDir, name string) (*SkillDef, error) {
	if name == "" {
		return nil, errors.New("skill: empty name")
	}
	path := filepath.Join(skillsDir, name, "SKILL.md")
	skill, err := readSkill(path, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("skill: %q not found at %s: %w", name, path, fs.ErrNotExist)
	}
	return skill, err
}

// readSkill parses the SKILL.md at path; a skill whose frontmatter names
// none is called fallbackName.
func readSkill(path, fallbackName string) (*SkillDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skill: read %s: %w", path, err)
	}
	doc := frontmatter.Parse(string(data))
	if doc.Err != nil {
		return nil, fmt.Errorf("skill: parse %s: %w", path, doc.Err)
	}
	return &SkillDef{
		Name:                   cmp.Or(doc.String("name"), fallbackName),
		Description:            doc.String("description"),
		DisableModelInvocation: doc.Bool("disable-model-invocation"),
		Body:                   strings.TrimSpace(doc.Body),
		Path:                   path,
		Dir:                    filepath.Dir(path),
	}, nil
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// SkillDiagnostics validates a skill against the Agent Skills metadata rules.
// A missing description prevents model discovery; other findings are warnings.
func SkillDiagnostics(skill *SkillDef) []string {
	if skill == nil {
		return nil
	}
	var diagnostics []string
	nameLength := len([]rune(skill.Name))
	if nameLength > 64 {
		diagnostics = append(diagnostics, fmt.Sprintf("name exceeds 64 characters (%d)", nameLength))
	}
	if !skillNamePattern.MatchString(skill.Name) {
		diagnostics = append(diagnostics, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(skill.Name, "-") || strings.HasSuffix(skill.Name, "-") {
		diagnostics = append(diagnostics, "name must not start or end with a hyphen")
	}
	if strings.Contains(skill.Name, "--") {
		diagnostics = append(diagnostics, "name must not contain consecutive hyphens")
	}
	descriptionLength := len([]rune(skill.Description))
	if strings.TrimSpace(skill.Description) == "" {
		diagnostics = append(diagnostics, "description is required")
	} else if descriptionLength > 1024 {
		diagnostics = append(diagnostics, fmt.Sprintf("description exceeds 1024 characters (%d)", descriptionLength))
	}
	return diagnostics
}

// DeduplicateSkills keeps the first definition for each public name, once
// Resource paths are ordered by precedence.
func DeduplicateSkills(defs []*SkillDef) []*SkillDef {
	result := make([]*SkillDef, 0, len(defs))
	seen := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		if _, exists := seen[def.Name]; exists {
			continue
		}
		seen[def.Name] = struct{}{}
		result = append(result, def)
	}
	return result
}

// LoadSkillPath loads a skill from a SKILL.md file path or a skill directory.
func LoadSkillPath(path string) (*SkillDef, error) {
	if path == "" {
		return nil, errors.New("skill: empty path")
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("skill: stat %s: %w", path, err)
	}
	if stat.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	}
	return readSkill(path, filepath.Base(filepath.Dir(path)))
}

// LoadSkillsFromPath loads one or more skills from a path.
// Supported forms:
//   - /path/to/skill/SKILL.md
//   - /path/to/skill-dir/          (contains SKILL.md)
//   - /path/to/skills-root/        (contains child dirs with SKILL.md)
func LoadSkillsFromPath(path string) ([]*SkillDef, error) {
	if path == "" {
		return nil, errors.New("skill: empty path")
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("skill: stat %s: %w", path, err)
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); !stat.IsDir() || err == nil {
		skill, err := LoadSkillPath(path)
		if err != nil {
			return nil, err
		}
		return []*SkillDef{skill}, nil
	}
	var out []*SkillDef
	var loadErrors []error
	seen := make(map[string]struct{})
	// coding/resources owns discovery for settings, CLI, and direct SDK
	// resource paths.
	for _, skillPath := range resources.DiscoverSkillDirs(path) {
		skill, err := LoadSkillPath(skillPath)
		if err != nil {
			loadErrors = append(loadErrors, err)
			continue
		}
		canonical := CanonicalizePath(skill.Path)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, skill)
	}
	slices.SortFunc(out, func(a, b *SkillDef) int { return strings.Compare(a.Name, b.Name) })
	return out, errors.Join(loadErrors...)
}

// DefaultSkillsDir returns the user agent's skills directory.
func DefaultSkillsDir() string {
	return filepath.Join(DefaultAgentDir(), "skills")
}

// ExpandSkillCommand expands a "/skill:name [args]" string to the skill XML
// block. Returns (expanded, true) on success, ("", false) if the text
// doesn't start with "/skill:" or the named skill isn't in skills.
//
// The XML block format:
//
//	<skill name="..." location="...">
//	References are relative to <dir>.
//
//	<body>
//	</skill>
//
// Args (if any) are appended after the block separated by two newlines.
func ExpandSkillCommand(text string, skills []*SkillDef) (string, bool) {
	if !strings.HasPrefix(text, "/skill:") {
		return "", false
	}
	rest := text[len("/skill:"):]
	skillName, args, _ := strings.Cut(rest, " ")
	skillName = strings.TrimSpace(skillName)
	args = strings.TrimSpace(args)

	var skill *SkillDef
	for _, s := range skills {
		if s.Name == skillName {
			skill = s
			break
		}
	}
	if skill == nil {
		return "", false
	}

	block := "<skill name=\"" + skill.Name + "\" location=\"" + skill.Path + "\">\n" +
		"References are relative to " + skill.Dir + ".\n\n" +
		skill.Body + "\n</skill>"
	if args != "" {
		return block + "\n\n" + args, true
	}
	return block, true
}

// skillBlockRe matches a skill block:
//
//	/^<skill name="([^"]+)" location="([^"]+)">\n([\s\S]*?)\n<\/skill>(?:\n\n([\s\S]+))?$/
var skillBlockRe = regexp.MustCompile(
	`^<skill name="([^"]+)" location="([^"]+)">\n([\s\S]*?)\n</skill>(?:\n\n([\s\S]+))?$`,
)

// ParsedSkillBlockFromText holds the parsed skill invocation data extracted
// from a user message text.
type ParsedSkillBlockFromText struct {
	Name        string
	Location    string
	Content     string
	UserMessage string // trailing user text after </skill>\n\n, or ""
}

// ParseSkillBlock attempts to parse a skill XML block from message text.
// Returns nil if the text doesn't match the skill block format.
func ParseSkillBlock(text string) *ParsedSkillBlockFromText {
	m := skillBlockRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	userMsg := strings.TrimSpace(m[4])
	return &ParsedSkillBlockFromText{
		Name:        m[1],
		Location:    m[2],
		Content:     m[3],
		UserMessage: userMsg,
	}
}
