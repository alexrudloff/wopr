package tui

// theme_loader.go: JSON theme file loading.
//
// Loads themes from opencode-compatible JSON files (dark.json, light.json,
// or custom themes). The schema:
//
//	{
//	  "name": "dark",
//	  "vars": { "cyan": "#00d7ff", ... },
//	  "colors": { "accent": "cyan", "border": "blue", ... },
//	  "export": { "pageBg": "#18181e", ... }
//	}
//
// A color value is a hex string, "", a 256-color index, or a variable name
// resolved recursively through "vars".

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/internal/text"
)

// LoadBuiltinTheme returns a built-in theme by name: "dark" and "light" are
// the default bundled theme resolved for that appearance.
func LoadBuiltinTheme(name string) (*Theme, error) {
	if name != "dark" && name != "light" {
		return nil, fmt.Errorf("builtin theme %q: not found", name)
	}
	theme, err := LoadBundledTheme(DefaultThemeName, TerminalTheme(name))
	if err != nil {
		return nil, err
	}
	theme.Name = name
	if theme.source != nil {
		theme.source.Name = name
	}
	return theme, nil
}

// ThemeJSON is the theme file schema.
type ThemeJSON struct {
	Name   string                     `json:"name"`
	Vars   map[string]ThemeColorValue `json:"vars"`
	Colors map[string]ThemeColorValue `json:"colors"`
	Export map[string]ThemeColorValue `json:"export,omitempty"`
	// colorKeys preserves the insertion order of keys in the "colors"
	// JSON object. Populated by extractColorKeyOrder after decoding.
	colorKeys []string
}

// LoadThemeFile reads a theme JSON file and returns the resolved Theme.
func LoadThemeFile(path string) (*Theme, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read theme %s: %w", path, err)
	}
	data := text.StripBomBytes(raw)
	if !json.Valid(data) {
		var syntax any
		return nil, fmt.Errorf("Failed to parse theme %s: %w", path, json.Unmarshal(data, &syntax))
	}
	if IsPaletteTheme(data) {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		tj, err := PaletteToThemeJSON(name, data, DetectTerminalBackground(TerminalThemeDetectionOptions{}).Theme)
		if err != nil {
			return nil, fmt.Errorf("theme %s: %w", path, err)
		}
		return resolveTheme(tj)
	}
	// wopr validates every vars/colors theme before use.
	if err := ValidateThemeJSON(path, data); err != nil {
		return nil, err
	}
	var tj ThemeJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("parse theme %s: %w", path, err)
	}
	tj.colorKeys = extractColorKeyOrder(data)
	return resolveTheme(&tj)
}

// ThemeColorValue is a theme color: a hex color, variable reference or empty
// string (Text), or a 256-color palette index (Index, when
// IsIndex is set).
type ThemeColorValue struct {
	Text    string
	Index   int
	IsIndex bool
	isSet   bool
}

// UnmarshalJSON accepts a JSON string or an integral JSON number. The 0..255
// range is enforced by ValidateThemeJSON for user-authored themes.
func (v *ThemeColorValue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*v = ThemeColorValue{Text: text, isSet: true}
		return nil
	}
	var number float64
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("theme color must be a string or 256-color index: %s", data)
	}
	if number != math.Trunc(number) {
		return fmt.Errorf("theme color index must be an integer: %s", data)
	}
	*v = ThemeColorValue{Index: int(number), IsIndex: true, isSet: true}
	return nil
}

// cssColor mirrors getResolvedThemeColors' per-value conversion. Indexed
// colors become approximate hex values and explicit empty colors use the
// caller's light/dark terminal-default fallback.
func (v ThemeColorValue) cssColor(defaultText string) string {
	if !v.isSet {
		return ""
	}
	if v.IsIndex {
		return ansi256ToHex(v.Index)
	}
	if v.Text == "" {
		return defaultText
	}
	return v.Text
}

// resolveVarRefs resolves variable references: indexes, "" and "#..."
// literals are final; any other string names a variable, resolved
// recursively.
func resolveVarRefs(value ThemeColorValue, vars map[string]ThemeColorValue, visited map[string]bool) (ThemeColorValue, error) {
	if value.IsIndex || value.Text == "" || strings.HasPrefix(value.Text, "#") {
		return value, nil
	}
	if visited[value.Text] {
		return ThemeColorValue{}, fmt.Errorf("Circular variable reference detected: %s", value.Text)
	}
	next, ok := vars[value.Text]
	if !ok {
		return ThemeColorValue{}, fmt.Errorf("Variable reference not found: %s", value.Text)
	}
	if visited == nil {
		visited = map[string]bool{}
	}
	visited[value.Text] = true
	return resolveVarRefs(next, vars, visited)
}

// LoadThemeDir scans a directory for .json theme files and returns a map
// of name → *Theme.
func LoadThemeDir(dir string) (map[string]*Theme, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	themes := make(map[string]*Theme)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Skip schema files.
		if strings.Contains(e.Name(), "schema") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		t, err := LoadThemeFile(path)
		if err != nil {
			continue // skip broken themes
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		themes[name] = t
	}
	return themes, nil
}

// extractColorKeyOrder parses the raw JSON to extract the key ordering of
// the "colors" object. json.Decoder preserves token order, unlike
// json.Unmarshal into map[string]string which loses insertion order.
func extractColorKeyOrder(data []byte) []string {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	// Find the "colors" key at the top level.
	var keys []string
	depth := 0
	inColors := false
	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{':
				if inColors && depth == 1 {
					// We're inside the colors object at depth 2.
					// Read key-value pairs.
					for dec.More() {
						kt, err := dec.Token()
						if err != nil {
							return keys
						}
						if key, ok := kt.(string); ok {
							keys = append(keys, key)
						}
						// Skip the value.
						if _, err := dec.Token(); err != nil {
							return keys
						}
					}
					return keys
				}
				depth++
			case '}':
				depth--
				inColors = false
			case '[':
				// skip arrays
			case ']':
				// skip arrays
			}
		case string:
			if depth == 1 && v == "colors" {
				inColors = true
			} else {
				inColors = false
			}
		}
	}
	return keys
}

// themeColorFallbacks lists the optional tokens and the token each falls back to,
// in resolution order.
var themeColorFallbacks = []struct{ token, from string }{
	{"scrollbarTrack", "muted"},
	{"scrollbarThumb", "text"},
	{"thinkingMax", "thinkingXhigh"},
	{"searchMatchBg", "selectedBg"},
	{"searchMatchText", "text"},
}

// resolveTheme converts a ThemeJSON to a Theme by resolving color references;
// an unresolvable reference fails the load.
func resolveTheme(tj *ThemeJSON) (*Theme, error) {
	return resolveThemeWithMode(tj, ColorModeTrueColor)
}

// resolveThemeWithMode is resolveTheme with an explicit color mode. The
// unmodified JSON is retained so the theme can be rebuilt in the other mode
// when truecolor support changes.
func resolveThemeWithMode(tj *ThemeJSON, mode ColorMode) (*Theme, error) {
	source := cloneThemeJSON(tj)
	// Build full colors map for Fg()/Bg() dynamic lookup, resolving in JSON
	// order so the first failing reference is the one reported.
	colorMap := make(map[string]ThemeColorValue, len(tj.Colors))
	for _, k := range tj.colorKeys {
		value, ok := tj.Colors[k]
		if !ok {
			continue
		}
		resolved, err := resolveVarRefs(value, tj.Vars, nil)
		if err != nil {
			return nil, err
		}
		colorMap[k] = resolved
	}
	fg := func(name string) string { return themeColorANSI(colorMap[name], mode, layerFg) }
	bg := func(name string) string { return themeColorANSI(colorMap[name], mode, layerBg) }
	// Optional tokens use nullish fallbacks: only an omitted token falls back,
	// while an explicitly configured value, including "", is retained.
	for _, fallback := range themeColorFallbacks {
		if _, present := tj.Colors[fallback.token]; present {
			continue
		}
		colorMap[fallback.token] = colorMap[fallback.from]
		tj.colorKeys = append(tj.colorKeys, fallback.token)
	}

	exportColors := resolveThemeExportColors(tj)

	return &Theme{
		Name:    tj.Name,
		Accent:  fg("accent"),
		Success: fg("success"),
		Error:   fg("error"),
		Warning: fg("warning"),
		Muted:   fg("muted"),
		Dim:     fg("dim"),
		Text:    fg("text"),

		Border:       fg("border"),
		BorderAccent: fg("borderAccent"),
		BorderMuted:  fg("borderMuted"),

		UserMessageBg:      bg("userMessageBg"),
		UserMessageText:    fg("userMessageText"),
		ToolPendingBg:      bg("toolPendingBg"),
		ToolSuccessBg:      bg("toolSuccessBg"),
		ToolErrorBg:        bg("toolErrorBg"),
		ToolTitle:          fg("toolTitle"),
		ToolOutput:         fg("toolOutput"),
		SelectedBg:         bg("selectedBg"),
		CustomMessageBg:    bg("customMessageBg"),
		CustomMessageText:  fg("customMessageText"),
		CustomMessageLabel: fg("customMessageLabel"),

		MDHeading:         fg("mdHeading"),
		MDLink:            fg("mdLink"),
		MDLinkUrl:         fg("mdLinkUrl"),
		MDCode:            fg("mdCode"),
		MDCodeBlock:       fg("mdCodeBlock"),
		MDCodeBlockBorder: fg("mdCodeBlockBorder"),
		MDQuote:           fg("mdQuote"),
		MDQuoteBorder:     fg("mdQuoteBorder"),
		MDHr:              fg("mdHr"),
		MDListBullet:      fg("mdListBullet"),

		ToolDiffAdded:   fg("toolDiffAdded"),
		ToolDiffRemoved: fg("toolDiffRemoved"),
		ToolDiffContext: fg("toolDiffContext"),

		SyntaxComment:     fg("syntaxComment"),
		SyntaxKeyword:     fg("syntaxKeyword"),
		SyntaxFunction:    fg("syntaxFunction"),
		SyntaxVariable:    fg("syntaxVariable"),
		SyntaxString:      fg("syntaxString"),
		SyntaxNumber:      fg("syntaxNumber"),
		SyntaxType:        fg("syntaxType"),
		SyntaxOperator:    fg("syntaxOperator"),
		SyntaxPunctuation: fg("syntaxPunctuation"),

		ThinkingText:    fg("thinkingText"),
		ThinkingOff:     fg("thinkingOff"),
		ThinkingMinimal: fg("thinkingMinimal"),
		ThinkingLow:     fg("thinkingLow"),
		ThinkingMedium:  fg("thinkingMedium"),
		ThinkingHigh:    fg("thinkingHigh"),
		ThinkingXhigh:   fg("thinkingXhigh"),

		BashMode: fg("bashMode"),

		ExportPageBg: exportColors["pageBg"],
		ExportCardBg: exportColors["cardBg"],
		ExportInfoBg: exportColors["infoBg"],

		BgClose:   SGRBgReset,
		Reset:     SGRResetAll,
		colors:    colorMap,
		colorKeys: tj.colorKeys,
		mode:      mode,
		source:    source,
	}, nil
}

func cloneThemeJSON(tj *ThemeJSON) *ThemeJSON {
	clone := *tj
	clone.colorKeys = slices.Clone(tj.colorKeys)
	return &clone
}

// resolveThemeExportColors returns the CSS colors for the export section,
// with indexes converted to hex and "" left unset.
// Any unresolvable reference leaves every export color unset.
func resolveThemeExportColors(tj *ThemeJSON) map[string]string {
	out := make(map[string]string, 3)
	for _, name := range []string{"pageBg", "cardBg", "infoBg"} {
		value, ok := tj.Export[name]
		if !ok {
			continue
		}
		resolved, err := resolveVarRefs(value, tj.Vars, nil)
		if err != nil {
			return nil
		}
		out[name] = resolved.cssColor("")
	}
	return out
}

// ColorMode is the terminal color depth a theme renders for.
type ColorMode string

const (
	ColorModeTrueColor ColorMode = "truecolor"
	ColorMode256       ColorMode = "256color"
)

// SGR color layers: foreground (38) and background (48).
const (
	layerFg = "38"
	layerBg = "48"
)

// themeColorANSI returns a foreground or background escape (layer): an index
// emits the 256-color escape, a hex emits 24-bit color in truecolor mode and
// its nearest palette index otherwise, and an explicit empty value emits the
// terminal-default reset. An absent token emits nothing.
func themeColorANSI(value ThemeColorValue, mode ColorMode, layer string) string {
	if !value.isSet {
		return ""
	}
	if value.IsIndex {
		return "\x1b[" + layer + ";5;" + strconv.Itoa(value.Index) + "m"
	}
	if value.Text == "" {
		if layer == layerFg {
			return SGRFgReset
		}
		return SGRBgReset
	}
	return hexColorANSI(value.Text, mode, layer)
}

// hexColorANSI converts "#rrggbb" to a 24-bit escape, or to the nearest
// palette index in 256-color mode. Invalid hex yields "".
func hexColorANSI(hex string, mode ColorMode, layer string) string {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return ""
	}
	if mode == ColorMode256 {
		return "\x1b[" + layer + ";5;" + strconv.Itoa(themeRGBTo256(r, g, b)) + "m"
	}
	return "\x1b[" + layer + ";2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}

// themeCubeValues and themeGrayValues are the channel levels of the xterm
// 256-color cube and grayscale ramp.
var (
	themeCubeValues = [6]int{0, 95, 135, 175, 215, 255}
	themeGrayValues = func() (values [24]int) {
		for i := range values {
			values[i] = 8 + i*10
		}
		return values
	}()
)

// themeClosestIndex mirrors findClosestCubeIndex/findClosestGrayIndex: the
// first value with the smallest absolute distance wins.
func themeClosestIndex(value int, values []int) int {
	minDist, minIdx := -1, 0
	for i, candidate := range values {
		dist := value - candidate
		if dist < 0 {
			dist = -dist
		}
		if minDist < 0 || dist < minDist {
			minDist, minIdx = dist, i
		}
	}
	return minIdx
}

// themeColorDistance is the weighted RGB distance. Each product is rounded
// separately, as JavaScript evaluates it, so no fused multiply-add changes
// a comparison.
func themeColorDistance(r1, g1, b1, r2, g2, b2 int) float64 {
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return float64(float64(dr*dr)*0.299) + float64(float64(dg*dg)*0.587) + float64(float64(db*db)*0.114)
}

// themeRGBTo256 returns the nearest 6x6x6 cube color, unless the color is
// nearly neutral (channel spread under 10) and the grayscale ramp is strictly
// closer.
func themeRGBTo256(r, g, b int) int {
	rIdx := themeClosestIndex(r, themeCubeValues[:])
	gIdx := themeClosestIndex(g, themeCubeValues[:])
	bIdx := themeClosestIndex(b, themeCubeValues[:])
	cubeIndex := 16 + 36*rIdx + 6*gIdx + bIdx
	cubeDist := themeColorDistance(r, g, b, themeCubeValues[rIdx], themeCubeValues[gIdx], themeCubeValues[bIdx])

	luma := float64(float64(0.299*float64(r))+float64(0.587*float64(g))) + float64(0.114*float64(b))
	gray := int(math.Floor(luma + 0.5)) // Math.round for non-negative values
	grayIdx := themeClosestIndex(gray, themeGrayValues[:])
	grayValue := themeGrayValues[grayIdx]
	grayDist := themeColorDistance(r, g, b, grayValue, grayValue, grayValue)

	spread := max(r, g, b) - min(r, g, b)
	if spread < 10 && grayDist < cubeDist {
		return 232 + grayIdx
	}
	return cubeIndex
}

// parseHex parses "#rrggbb" to (r, g, b, true).
func parseHex(hex string) (int, int, int, bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	r, err1 := strconv.ParseInt(hex[0:2], 16, 32)
	g, err2 := strconv.ParseInt(hex[2:4], 16, 32)
	b, err3 := strconv.ParseInt(hex[4:6], 16, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return int(r), int(g), int(b), true
}
