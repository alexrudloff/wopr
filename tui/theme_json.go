package tui

// theme_json.go: the check for user-authored theme files.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// themeColorTokens lists the theme color tokens; optional ones may be
// omitted.
var themeColorTokens = []struct {
	name     string
	optional bool
}{
	{"accent", false}, {"border", false}, {"borderAccent", false}, {"borderMuted", false},
	{"success", false}, {"error", false}, {"warning", false}, {"muted", false}, {"dim", false},
	{"text", false}, {"thinkingText", false},
	{"scrollbarTrack", true}, {"scrollbarThumb", true},
	{"selectedBg", false}, {"searchMatchBg", true}, {"searchMatchText", true},
	{"userMessageBg", false}, {"userMessageText", false}, {"customMessageBg", false},
	{"customMessageText", false}, {"customMessageLabel", false}, {"toolPendingBg", false},
	{"toolSuccessBg", false}, {"toolErrorBg", false}, {"toolTitle", false}, {"toolOutput", false},
	{"mdHeading", false}, {"mdLink", false}, {"mdLinkUrl", false}, {"mdCode", false},
	{"mdCodeBlock", false}, {"mdCodeBlockBorder", false}, {"mdQuote", false},
	{"mdQuoteBorder", false}, {"mdHr", false}, {"mdListBullet", false},
	{"toolDiffAdded", false}, {"toolDiffRemoved", false}, {"toolDiffContext", false},
	{"syntaxComment", false}, {"syntaxKeyword", false}, {"syntaxFunction", false},
	{"syntaxVariable", false}, {"syntaxString", false}, {"syntaxNumber", false},
	{"syntaxType", false}, {"syntaxOperator", false}, {"syntaxPunctuation", false},
	{"thinkingOff", false}, {"thinkingMinimal", false}, {"thinkingLow", false},
	{"thinkingMedium", false}, {"thinkingHigh", false}, {"thinkingXhigh", false},
	{"thinkingMax", true},
	{"bashMode", false},
}

// ValidateThemeJSON checks one theme document (JSON text without a BOM): a
// name without "/", every required color token, and each color, variable,
// and export value a string or an integer 0-255. It returns an error naming
// what is wrong, or nil.
func ValidateThemeJSON(label string, data []byte) error {
	var doc struct {
		Name   *string                    `json:"name"`
		Vars   map[string]json.RawMessage `json:"vars"`
		Colors map[string]json.RawMessage `json:"colors"`
		Export map[string]json.RawMessage `json:"export"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("Invalid theme %q: %w", label, err)
	}
	var missing, other []string
	if doc.Name == nil {
		other = append(other, "  - /name: required")
	}
	if doc.Colors == nil {
		other = append(other, "  - /colors: required")
	} else {
		for _, token := range themeColorTokens {
			if _, ok := doc.Colors[token.name]; !ok && !token.optional {
				missing = append(missing, "  - "+token.name)
			}
		}
	}
	for _, section := range []struct {
		name   string
		values map[string]json.RawMessage
	}{{"vars", doc.Vars}, {"colors", doc.Colors}, {"export", doc.Export}} {
		for _, key := range slices.Sorted(maps.Keys(section.values)) {
			if !validThemeColorValue(section.values[key]) {
				other = append(other, "  - /"+section.name+"/"+key+": must be a string or an integer 0-255")
			}
		}
	}
	if len(missing) > 0 || len(other) > 0 {
		var b strings.Builder
		b.WriteString("Invalid theme \"" + label + "\":\n")
		if len(missing) > 0 {
			b.WriteString("\nMissing required color tokens:\n" + strings.Join(missing, "\n"))
			b.WriteString("\n\nPlease add these colors to your theme's \"colors\" object.")
			b.WriteString("\nSee the built-in themes (dark.json, light.json) for reference values.")
		}
		if len(other) > 0 {
			b.WriteString("\n\nOther errors:\n" + strings.Join(other, "\n"))
		}
		return errors.New(b.String())
	}
	if strings.Contains(*doc.Name, "/") {
		return errors.New("Invalid theme name \"" + *doc.Name + "\": theme names cannot contain \"/\" because it is reserved for automatic light/dark theme settings.")
	}
	return nil
}

// validThemeColorValue reports whether value is a string or an integer in
// 0-255.
func validThemeColorValue(value json.RawMessage) bool {
	var s string
	if json.Unmarshal(value, &s) == nil {
		return true
	}
	var n float64
	return json.Unmarshal(value, &n) == nil && n == float64(int(n)) && n >= 0 && n <= 255
}
