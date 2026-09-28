package tui

// theme_bundle.go: the bundled theme set.
//
// Bundled themes use the palette format of themes/*.json: a "defs" table of
// named hex colors and a "theme" table whose values are a hex color, a def or
// theme-token reference, a 256-color index, "none"/"transparent" (terminal
// default), or a {"dark": ..., "light": ...} pair resolved by the terminal's
// appearance. Each palette converts to a regular ThemeJSON: the resolved
// palette tokens become vars, wopr's component tokens map onto them, and the
// palette tokens are also exposed under their own names (background,
// backgroundPanel, primary, ...) so components can style with them directly.

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
)

//go:embed themes/*.json
var bundledThemeFS embed.FS

// DefaultThemeName is the bundled theme used when no theme is configured.
const DefaultThemeName = "wopr"

type paletteJSON struct {
	Defs  map[string]json.RawMessage `json:"defs"`
	Theme map[string]json.RawMessage `json:"theme"`
}

// paletteTokens are the palette keys every bundled theme defines, exposed on
// the resolved theme under the same names. "accent" is exposed as
// "accentAlt" because wopr's "accent" token is the palette's primary color.
var paletteTokens = []string{
	"primary", "secondary", "accent", "error", "warning", "success", "info",
	"text", "textMuted", "selectedListItemText",
	"background", "backgroundPanel", "backgroundElement", "backgroundMenu",
	"border", "borderActive", "borderSubtle",
	"diffAdded", "diffRemoved", "diffContext", "diffHunkHeader",
	"diffHighlightAdded", "diffHighlightRemoved",
	"diffAddedBg", "diffRemovedBg", "diffContextBg", "diffLineNumber",
	"diffAddedLineNumberBg", "diffRemovedLineNumberBg",
	"markdownText", "markdownHeading", "markdownLink", "markdownLinkText",
	"markdownCode", "markdownBlockQuote", "markdownEmph", "markdownStrong",
	"markdownHorizontalRule", "markdownListItem", "markdownListEnumeration",
	"markdownImage", "markdownImageText", "markdownCodeBlock",
	"syntaxComment", "syntaxKeyword", "syntaxFunction", "syntaxVariable",
	"syntaxString", "syntaxNumber", "syntaxType", "syntaxOperator", "syntaxPunctuation",
}

// componentTokens maps wopr's component tokens onto palette tokens.
var componentTokens = [][2]string{
	{"accent", "primary"},
	{"border", "border"},
	{"borderAccent", "borderActive"},
	{"borderMuted", "borderSubtle"},
	{"success", "success"},
	{"error", "error"},
	{"warning", "warning"},
	{"muted", "textMuted"},
	{"dim", "textMuted"},
	{"text", "text"},
	{"thinkingText", "textMuted"},

	{"selectedBg", "backgroundElement"},
	{"scrollbarTrack", "borderSubtle"},
	{"scrollbarThumb", "textMuted"},
	{"searchMatchBg", "warning"},
	{"searchMatchText", "background"},
	{"userMessageBg", "backgroundPanel"},
	{"userMessageText", "text"},
	{"customMessageBg", "backgroundPanel"},
	{"customMessageText", "text"},
	{"customMessageLabel", "secondary"},
	{"toolPendingBg", "backgroundPanel"},
	{"toolSuccessBg", "backgroundPanel"},
	{"toolErrorBg", "backgroundPanel"},
	{"toolTitle", "text"},
	{"toolOutput", "textMuted"},

	{"mdHeading", "markdownHeading"},
	{"mdLink", "markdownLink"},
	{"mdLinkUrl", "markdownLinkText"},
	{"mdCode", "markdownCode"},
	{"mdCodeBlock", "markdownCodeBlock"},
	{"mdCodeBlockBorder", "border"},
	{"mdQuote", "markdownBlockQuote"},
	{"mdQuoteBorder", "markdownBlockQuote"},
	{"mdHr", "markdownHorizontalRule"},
	{"mdListBullet", "markdownListItem"},

	{"toolDiffAdded", "diffAdded"},
	{"toolDiffRemoved", "diffRemoved"},
	{"toolDiffContext", "diffContext"},

	{"syntaxComment", "syntaxComment"},
	{"syntaxKeyword", "syntaxKeyword"},
	{"syntaxFunction", "syntaxFunction"},
	{"syntaxVariable", "syntaxVariable"},
	{"syntaxString", "syntaxString"},
	{"syntaxNumber", "syntaxNumber"},
	{"syntaxType", "syntaxType"},
	{"syntaxOperator", "syntaxOperator"},
	{"syntaxPunctuation", "syntaxPunctuation"},

	{"thinkingOff", "borderSubtle"},
	{"thinkingMinimal", "textMuted"},
	{"thinkingLow", "info"},
	{"thinkingMedium", "secondary"},
	{"thinkingHigh", "accentAlt"},
	{"thinkingXhigh", "warning"},
	{"thinkingMax", "error"},

	{"bashMode", "warning"},
}

// BundledThemeNames lists the bundled themes in alphabetical order.
func BundledThemeNames() []string {
	entries, err := bundledThemeFS.ReadDir("themes")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	slices.Sort(names)
	return names
}

// LoadBundledTheme resolves a bundled theme for a terminal appearance ("dark"
// or "light"). The resolved theme is named name.
func LoadBundledTheme(name string, appearance TerminalTheme) (*Theme, error) {
	data, err := bundledThemeFS.ReadFile(path.Join("themes", name+".json"))
	if err != nil {
		return nil, fmt.Errorf("bundled theme %q: %w", name, err)
	}
	tj, err := PaletteToThemeJSON(name, data, appearance)
	if err != nil {
		return nil, err
	}
	return resolveTheme(tj)
}

// IsPaletteTheme reports whether data is a palette-format theme (a "theme"
// object) rather than a vars/colors theme.
func IsPaletteTheme(data []byte) bool {
	var probe struct {
		Theme  json.RawMessage `json:"theme"`
		Colors json.RawMessage `json:"colors"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return false
	}
	return len(probe.Theme) > 0 && probe.Theme[0] == '{' && len(probe.Colors) == 0
}

// PaletteToThemeJSON converts a palette-format theme into a ThemeJSON for the
// given appearance.
func PaletteToThemeJSON(name string, data []byte, appearance TerminalTheme) (*ThemeJSON, error) {
	var palette paletteJSON
	if err := json.Unmarshal(data, &palette); err != nil {
		return nil, fmt.Errorf("parse theme %q: %w", name, err)
	}
	mode := "dark"
	if appearance == TerminalTheme("light") {
		mode = "light"
	}
	vars := make(map[string]ThemeColorValue, len(paletteTokens)+2)
	resolve := func(token string) (ThemeColorValue, error) {
		raw, ok := palette.Theme[token]
		if !ok {
			return ThemeColorValue{}, fmt.Errorf("theme %q: missing color %q", name, token)
		}
		return resolvePaletteColor(raw, &palette, mode, nil)
	}
	for _, token := range paletteTokens {
		if _, ok := palette.Theme[token]; !ok {
			continue
		}
		value, err := resolve(token)
		if err != nil {
			return nil, err
		}
		vars[token] = value
	}
	for _, required := range []string{"primary", "text", "textMuted", "background", "backgroundPanel", "backgroundElement", "border"} {
		if _, ok := vars[required]; !ok {
			return nil, fmt.Errorf("theme %q: missing color %q", name, required)
		}
	}
	fallback := func(token, from string) {
		if _, ok := vars[token]; !ok {
			vars[token] = vars[from]
		}
	}
	fallback("selectedListItemText", "background")
	// Selected rows paint selectedListItemText on primary; a theme whose pair
	// is hard to read there gets its background color instead.
	if contrastRatio(vars["selectedListItemText"].Text, vars["primary"].Text) < minSelectedContrast {
		vars["selectedListItemText"] = vars["background"]
	}
	fallback("backgroundMenu", "backgroundElement")
	fallback("borderActive", "primary")
	fallback("borderSubtle", "border")
	for _, token := range []string{"secondary", "accent", "info"} {
		fallback(token, "primary")
	}
	for _, token := range []string{"error", "warning", "success"} {
		fallback(token, "text")
	}
	vars["accentAlt"] = vars["accent"]
	for _, token := range paletteTokens {
		if _, ok := vars[token]; !ok {
			// Optional palette tokens default to their nearest core color.
			switch {
			case strings.HasPrefix(token, "diff") && strings.HasSuffix(token, "Bg"):
				vars[token] = vars["backgroundPanel"]
			case strings.HasPrefix(token, "syntax"), strings.HasPrefix(token, "markdown"), strings.HasPrefix(token, "diff"):
				vars[token] = vars["text"]
			}
		}
	}

	tj := &ThemeJSON{Name: name, Vars: vars, Colors: map[string]ThemeColorValue{}}
	set := func(token, ref string) {
		if _, ok := tj.Colors[token]; !ok {
			tj.colorKeys = append(tj.colorKeys, token)
		}
		tj.Colors[token] = ThemeColorValue{Text: ref, isSet: true}
	}
	for _, pair := range componentTokens {
		set(pair[0], pair[1])
	}
	for _, token := range paletteTokens {
		if token == "accent" {
			set("accentAlt", "accentAlt")
			continue
		}
		if _, mapped := tj.Colors[token]; !mapped {
			set(token, token)
		}
	}
	tj.Export = map[string]ThemeColorValue{
		"pageBg": vars["background"],
		"cardBg": vars["backgroundPanel"],
		"infoBg": vars["backgroundElement"],
	}
	return tj, nil
}

func resolvePaletteColor(raw json.RawMessage, palette *paletteJSON, mode string, chain []string) (ThemeColorValue, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		switch {
		case text == "none" || text == "transparent" || text == "":
			return ThemeColorValue{Text: "", isSet: true}, nil
		case strings.HasPrefix(text, "#"):
			return ThemeColorValue{Text: text, isSet: true}, nil
		}
		if slices.Contains(chain, text) {
			return ThemeColorValue{}, fmt.Errorf("circular color reference: %s -> %s", strings.Join(chain, " -> "), text)
		}
		next, ok := palette.Defs[text]
		if !ok {
			next, ok = palette.Theme[text]
		}
		if !ok {
			return ThemeColorValue{}, fmt.Errorf("color reference %q not found in defs or theme", text)
		}
		return resolvePaletteColor(next, palette, mode, append(chain, text))
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		if number != math.Trunc(number) || number < 0 || number > 255 {
			return ThemeColorValue{}, fmt.Errorf("invalid color index %s", raw)
		}
		return ThemeColorValue{Index: int(number), IsIndex: true, isSet: true}, nil
	}
	var variant map[string]json.RawMessage
	if err := json.Unmarshal(raw, &variant); err != nil {
		return ThemeColorValue{}, fmt.Errorf("invalid color value %s", raw)
	}
	chosen, ok := variant[mode]
	if !ok {
		return ThemeColorValue{}, fmt.Errorf("color variant missing %q", mode)
	}
	return resolvePaletteColor(chosen, palette, mode, chain)
}

// minSelectedContrast is the least WCAG contrast ratio kept between selected
// row text and the primary color it sits on (3:1, large-text AA).
const minSelectedContrast = 3.0

// contrastRatio is the WCAG contrast ratio of two "#rrggbb" colors, or the
// maximum when either is not a hex color (indexed and terminal-default
// colors are left alone).
func contrastRatio(a, b string) float64 {
	la, okA := relativeLuminance(a)
	lb, okB := relativeLuminance(b)
	if !okA || !okB {
		return 21
	}
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func relativeLuminance(hex string) (float64, bool) {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return 0, false
	}
	channel := func(v int) float64 {
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b), true
}
