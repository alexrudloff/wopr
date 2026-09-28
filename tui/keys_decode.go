package tui

// keys_decode.go: printable key decoding helpers for TUI components.
//
// Decodes printable CSI-u / modifyOtherKeys sequences for the Input component.

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const kittyPrintableAllowedModifiers = modShift | lockMask

var kittyCSIURegex = regexp.MustCompile(`^\x1b\[(\d+)(?::(\d*))?(?::(\d+))?(?:;(\d+))?(?::(\d+))?u$`)

var kittyFunctionalKeyEquivalents = map[int]int{
	57399: 48, // KP_0 -> 0
	57400: 49, // KP_1 -> 1
	57401: 50,
	57402: 51,
	57403: 52,
	57404: 53,
	57405: 54,
	57406: 55,
	57407: 56,
	57408: 57,
	57409: 46, // .
	57410: 47, // /
	57411: 42, // *
	57412: 45, // -
	57413: 43, // +
	57415: 61, // =
	57416: 44, // ,
	57417: -4, // left
	57418: -3, // right
	57419: -1, // up
	57420: -2, // down
	57421: -12,
	57422: -13,
	57423: -14,
	57424: -15,
	57425: -11,
	57426: -10,
}

func normalizeKittyFunctionalCodepoint(codepoint int) int {
	if mapped, ok := kittyFunctionalKeyEquivalents[codepoint]; ok {
		return mapped
	}
	return codepoint
}

func parseOptionalInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// DecodeKittyPrintable decodes a printable Kitty CSI-u sequence into the
// corresponding character.
func DecodeKittyPrintable(data string) (string, bool) {
	match := kittyCSIURegex.FindStringSubmatch(data)
	if match == nil {
		return "", false
	}
	codepoint, err := strconv.Atoi(match[1])
	if err != nil {
		return "", false
	}
	shiftedKey, hasShiftedKey := parseOptionalInt(match[2])
	modValue := 1
	if match[4] != "" {
		parsed, err := strconv.Atoi(match[4])
		if err != nil {
			return "", false
		}
		modValue = parsed
	}
	modifier := modValue - 1
	if modifier&^kittyPrintableAllowedModifiers != 0 {
		return "", false
	}
	if modifier&(modAlt|modCtrl) != 0 {
		return "", false
	}
	effectiveCodepoint := codepoint
	if modifier&modShift != 0 {
		if hasShiftedKey {
			effectiveCodepoint = shiftedKey
		} else if codepoint >= 'a' && codepoint <= 'z' {
			// Shift+letter is layout-independent: uppercase the base when the
			// terminal reports the base codepoint without a shifted alternate
			// (e.g. \x1b[97;2u for Shift+a). Otherwise capital letters are lost.
			effectiveCodepoint = codepoint - 32
		}
	}
	effectiveCodepoint = normalizeKittyFunctionalCodepoint(effectiveCodepoint)
	// Codepoints above U+10FFFF are not valid characters.
	if effectiveCodepoint < 32 || effectiveCodepoint > unicode.MaxRune {
		return "", false
	}
	return string(rune(effectiveCodepoint)), true
}

func decodeModifyOtherKeysPrintable(data string) (string, bool) {
	parsed := parseModifyOtherKeys(data)
	if parsed == nil || parsed.modifier&^modShift != 0 {
		return "", false
	}
	if parsed.codepoint < 32 || parsed.codepoint > unicode.MaxRune {
		return "", false
	}
	return string(rune(parsed.codepoint)), true
}

// DecodePrintableKey decodes printable terminal sequences from either Kitty
// CSI-u or xterm modifyOtherKeys formats.
func DecodePrintableKey(data string) (string, bool) {
	if s, ok := DecodeKittyPrintable(data); ok {
		return s, true
	}
	return decodeModifyOtherKeysPrintable(data)
}

// ShouldDeliverKey reports whether a raw input chunk may be handed to a focused
// component's HandleInput. Kitty key releases are dropped unless the component
// implements KeyReleaseReceiver and opts in.
//
// This is the single decision point for focused-component key delivery, and
// every such handoff must route through it. A component that acts on a release
// fires each keystroke twice, because extendedKeyInit pushes \x1b[>7u, whose
// flag 2 makes the terminal report press, repeat, and release. Scattering the
// check across dispatch sites is what let extension dialogs move a selector
// cursor two rows per arrow press: the filter existed, but an earlier return
// bypassed it.
//
// Raw-input consumers are a different contract and must not use this: terminal
// input listeners, alt-screen viewport handling, and extension shortcut
// listeners all see unfiltered input by design.
func ShouldDeliverKey(component Component, data string) bool {
	if !IsKeyRelease(data) {
		return true
	}
	receiver, ok := component.(KeyReleaseReceiver)
	return ok && receiver.WantsKeyRelease()
}

// IsKeyRelease reports whether a raw input chunk is a Kitty key-release event
// (flag 2, ":3" variants). Bracketed-paste content is never treated as a
// release even when it contains ":3" byte patterns.
//
// Prefer ShouldDeliverKey when routing input to a focused component; call this
// directly only from a raw-input consumer.
func IsKeyRelease(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	return slices.ContainsFunc(releaseSuffixes, func(suf string) bool { return strings.Contains(data, suf) })
}

// releaseSuffixes are the Kitty ":3" event-type markers of a key release.
var releaseSuffixes = []string{":3u", ":3~", ":3A", ":3B", ":3C", ":3D", ":3H", ":3F"}
