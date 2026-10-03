// Package export provides HTML session export from embedded template files.
package export

import (
	"cmp"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/tui"

	"github.com/alexrudloff/wopr/internal/codingagent/sessionblob"
)

const appName = "wopr"

//go:embed assets/template.html assets/template.css assets/template.js assets/vendor/marked.min.js assets/vendor/highlight.min.js
var templateFS embed.FS

// SessionData is the HTML export payload.
// Header and Entries use json.RawMessage to preserve the original JSONL
// key ordering; Go's map[string]any would sort keys alphabetically on Marshal.
type SessionData struct {
	Header  json.RawMessage   `json:"header"`
	Entries []json.RawMessage `json:"entries"`
	LeafID  *string           `json:"leafId"`
}

// defaultTextColor is the fallback for empty-string color tokens in the
// theme: "" → defaultText ("#e5e5e7" for dark, "#000000" for light).
const defaultTextColorDark = "#e5e5e7"
const defaultTextColorLight = "#000000"

func generateThemeVars() string {
	th := tui.ActiveTheme()
	colors := th.Colors()
	if colors == nil {
		return ""
	}
	// Use ColorKeys() to preserve the theme's JSON insertion order.
	// Fall back to sorted keys if colorKeys is not available.
	keys := th.ColorKeys()
	if len(keys) == 0 {
		keys = make([]string, 0, len(colors))
		for k := range colors {
			keys = append(keys, k)
		}
		slices.Sort(keys)
	}
	// Empty strings resolve to the default text color for the theme variant (dark=#e5e5e7, light=#000000).
	defaultText := defaultTextColorDark
	if th.Name == "light" {
		defaultText = defaultTextColorLight
	}
	var lines []string
	for _, k := range keys {
		v := cmp.Or(colors[k], defaultText)
		lines = append(lines, fmt.Sprintf("--%s: %s;", k, v))
	}
	pageBg, cardBg, infoBg := exportBackgrounds(th)
	lines = append(lines,
		fmt.Sprintf("--exportPageBg: %s;", pageBg),
		fmt.Sprintf("--exportCardBg: %s;", cardBg),
		fmt.Sprintf("--exportInfoBg: %s;", infoBg),
	)
	return strings.Join(lines, "\n      ")
}

// exportBackgrounds returns the theme's page, card, and info backgrounds,
// with the dark defaults for any it leaves unset.
func exportBackgrounds(th *tui.Theme) (page, card, info string) {
	return cmp.Or(th.ExportPageBg, "#18181e"), cmp.Or(th.ExportCardBg, "#1e1e24"), cmp.Or(th.ExportInfoBg, "#3c3728")
}

// ToHTML converts session data to self-contained SPA HTML.
func ToHTML(data SessionData) string {
	template := mustReadAsset("assets/template.html")
	css := mustReadAsset("assets/template.css")
	js := mustReadAsset("assets/template.js")
	marked := mustReadAsset("assets/vendor/marked.min.js")
	highlight := mustReadAsset("assets/vendor/highlight.min.js")

	bodyBg, containerBg, infoBg := exportBackgrounds(tui.ActiveTheme())
	css = strings.ReplaceAll(css, "{{THEME_VARS}}", generateThemeVars())
	css = strings.ReplaceAll(css, "{{BODY_BG}}", bodyBg)
	css = strings.ReplaceAll(css, "{{CONTAINER_BG}}", containerBg)
	css = strings.ReplaceAll(css, "{{INFO_BG}}", infoBg)

	payload, _ := json.Marshal(data)
	sessionDataBase64 := base64.StdEncoding.EncodeToString(payload)

	// The template placeholders are substituted with JavaScript
	// String.replace semantics ($& and $$ are special), not literally.
	out := template
	out = jsReplace(out, "{{CSS}}", css)
	out = jsReplace(out, "{{JS}}", js)
	out = jsReplace(out, "{{SESSION_DATA}}", sessionDataBase64)
	out = jsReplace(out, "{{MARKED_JS}}", marked)
	out = jsReplace(out, "{{HIGHLIGHT_JS}}", highlight)
	return out
}

func mustReadAsset(path string) string {
	b, err := templateFS.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// jsReplace replicates JavaScript's String.prototype.replace(search, replacement)
// semantics for the $ special patterns in the replacement string:
//   - $$ → literal "$"
//   - $& → the matched search string
//   - $` → portion of the string before the match
//   - $' → portion of the string after the match
//
// The injected vendor scripts depend on it: highlight.min.js contains $& in
// regex patterns, and template.js uses $$ for literal $ in template literals.
func jsReplace(s, search, replacement string) string {
	before, after, ok := strings.Cut(s, search)
	if !ok {
		return s
	}
	// $` and $' refer to the first occurrence.
	special := map[byte]string{'$': "$", '&': search, '`': before, '\'': after}
	var b strings.Builder
	for i := 0; i < len(replacement); i++ {
		if replacement[i] == '$' && i+1 < len(replacement) {
			if sub, ok := special[replacement[i+1]]; ok {
				b.WriteString(sub)
				i++
				continue
			}
		}
		b.WriteByte(replacement[i])
	}
	processed := b.String()
	return strings.ReplaceAll(s, search, processed)
}

// FromJSONL converts raw session JSONL bytes into export SessionData.
// Uses json.RawMessage to preserve the original key ordering from the JSONL.
func FromJSONL(data []byte) (SessionData, error) {
	lines := strings.Split(string(data), "\n")
	var sd SessionData
	first := true
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Validate JSON before storing as RawMessage.
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			if first {
				return SessionData{}, fmt.Errorf("parse header: %w", err)
			}
			continue
		}
		if first {
			first = false
			if typ, _ := m["type"].(string); typ != "session" {
				return SessionData{}, fmt.Errorf("missing session header")
			}
			sd.Header = json.RawMessage(line)
			continue
		}
		shrunk, _, _ := sessionblob.Shrink([]byte(line))
		sd.Entries = append(sd.Entries, json.RawMessage(shrunk))
		if id, _ := m["id"].(string); id != "" {
			sd.LeafID = &id
		}
	}
	if first {
		return SessionData{}, fmt.Errorf("empty session file")
	}
	return sd, nil
}

// ExportFromFile reads a session JSONL file and writes the HTML export.
func ExportFromFile(inputPath, outputPath string) (string, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return "", fmt.Errorf("read session: %w", err)
	}
	sd, err := FromJSONL(sessionblob.ResolveFile(inputPath, data))
	if err != nil {
		return "", fmt.Errorf("parse session: %w", err)
	}
	htmlStr := ToHTML(sd)
	if outputPath == "" {
		base := strings.TrimSuffix(filepath.Base(inputPath), ".jsonl")
		outputPath = fmt.Sprintf("%s-session-%s.html", appName, base)
	}
	if err := os.WriteFile(outputPath, []byte(htmlStr), 0o644); err != nil {
		return "", fmt.Errorf("write html: %w", err)
	}
	return outputPath, nil
}
