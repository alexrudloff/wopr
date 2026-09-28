package tui

// key_match.go: Dynamic key matching.
//
// matchesKeyDynamic resolves any KeyID string (e.g. "ctrl+shift+left")
// against raw terminal input by parsing both sides and comparing
// codepoints + modifiers. This replaces the static tuiKeyIDInputs map
// for extension shortcuts and handles any modifier+key combination.
//
// Supports three terminal key protocols:
//   - Kitty CSI-u:         \x1b[<codepoint>[:<shifted>[:<base>]];<modifier>u
//   - xterm modifyOtherKeys: \x1b[27;<modifier>;<codepoint>~
//   - Legacy CSI sequences: \x1b[1;<modifier><final> for arrows, \x1b[<n>;<modifier>~ for functional keys

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Modifier bit constants: matches Kitty keyboard protocol.
// Modifier values in CSI sequences are 1-indexed (wire = 1 + bitmask).
const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
	modSuper = 8

	// Lock bits to mask out when comparing modifiers.
	lockMask = 64 + 128 // Caps Lock + Num Lock
)

// Sentinel codepoints for non-printable keys (negative = not real Unicode).
const (
	cpArrowUp    = -1
	cpArrowDown  = -2
	cpArrowRight = -3
	cpArrowLeft  = -4

	cpDelete   = -10
	cpInsert   = -11
	cpPageUp   = -12
	cpPageDown = -13
	cpHome     = -14
	cpEnd      = -15

	cpEscape    = 27
	cpTab       = 9
	cpEnter     = 13
	cpSpace     = 32
	cpBackspace = 127
)

// arrowFinals maps arrow final bytes to sentinel codepoints.
var arrowFinals = map[byte]int{
	'A': cpArrowUp,
	'B': cpArrowDown,
	'C': cpArrowRight,
	'D': cpArrowLeft,
}

// homeEndFinals maps H/F final bytes to sentinel codepoints.
var homeEndFinals = map[byte]int{
	'H': cpHome,
	'F': cpEnd,
}

// funcKeyCodes maps the CSI parameter to sentinel codepoints.
var funcKeyCodes = map[int]int{
	2: cpInsert,
	3: cpDelete,
	5: cpPageUp,
	6: cpPageDown,
	7: cpHome,
	8: cpEnd,
}

// keyNameCodepoints maps KeyID base key names to (codepoint, isArrow) pairs.
// "isArrow" means the key uses \x1b[1;<mod><final> format rather than CSI-u.
var keyNameCodepoints = map[string]struct {
	cp      int
	isArrow bool
}{
	// Arrows
	"up":    {cpArrowUp, true},
	"down":  {cpArrowDown, true},
	"left":  {cpArrowLeft, true},
	"right": {cpArrowRight, true},

	// Functional keys (CSI <n>;<mod>~ format)
	"delete":   {cpDelete, false},
	"insert":   {cpInsert, false},
	"pageup":   {cpPageUp, false},
	"pagedown": {cpPageDown, false},
	"home":     {cpHome, false},
	"end":      {cpEnd, false},

	// Special named keys
	"escape":    {cpEscape, false},
	"esc":       {cpEscape, false},
	"tab":       {cpTab, false},
	"enter":     {cpEnter, false},
	"return":    {cpEnter, false},
	"space":     {cpSpace, false},
	"backspace": {cpBackspace, false},
}

// Reverse lookups: sentinel codepoint → CSI final byte / parameter number.
var (
	arrowCPtoFinal = invert(arrowFinals)
	funcCPtoNum    = invert(funcKeyCodes)
	homeCPtoFinal  = invert(homeEndFinals)
)

// invert swaps a 1:1 map's keys and values.
func invert[K, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// cpKPEnter is the Kitty keypad Enter codepoint.
const cpKPEnter = 57414

// parsedKey is the result of parsing a CSI sequence from terminal input.
type parsedKey struct {
	codepoint int
	modifier  int // bitmask (0-indexed): shift=1, alt=2, ctrl=4, super=8
	// baseLayoutKey is the Kitty flag-4 alternate that names the key's
	// position on a standard PC-101 layout.
	baseLayoutKey    int
	hasBaseLayoutKey bool
}

// parseCSIu parses a Kitty CSI-u sequence:
//
//	\x1b[<codepoint>[:<shifted>[:<base>]][;<modifier>[:<event>]]u
//
// Flag 4 appends the shifted key and the base-layout key after the codepoint
// (\x1b[1089::99;5u is Ctrl+С on a Cyrillic layout, base key c). The base-layout
// key is retained for matchesKittySequence; the shifted key only matters for
// printable decoding (DecodeKittyPrintable). Returns nil if data is not CSI-u.
func parseCSIu(data string) *parsedKey {
	match := kittyCSIURegex.FindStringSubmatch(data)
	if match == nil {
		return nil
	}
	cp, err := strconv.Atoi(match[1])
	if err != nil {
		return nil
	}
	modValue := 1
	if match[4] != "" {
		if modValue, err = strconv.Atoi(match[4]); err != nil {
			return nil
		}
	}
	pk := &parsedKey{codepoint: cp, modifier: (modValue - 1) &^ lockMask}
	pk.baseLayoutKey, pk.hasBaseLayoutKey = parseOptionalInt(match[3])
	return pk
}

// normalizeShiftedLetterIdentityCodepoint: with Shift held an
// uppercase A-Z codepoint names the same key as its lowercase letter.
func normalizeShiftedLetterIdentityCodepoint(codepoint, modifier int) int {
	if (modifier&^lockMask)&modShift != 0 && codepoint >= 'A' && codepoint <= 'Z' {
		return codepoint + 32
	}
	return codepoint
}

// isSymbolCodepoint reports whether cp (truncated to 16 bits) is a symbol key.
func isSymbolCodepoint(cp int) bool {
	unit := cp & 0xFFFF
	return unit < 128 && isSymbolKey(byte(unit))
}

// matchesKittySequence matches a parsed
// CSI-u key: modifiers must match exactly, and the codepoint matches after
// keypad and shifted-letter normalization. Failing that, the base-layout key
// matches so Ctrl+С on a Cyrillic layout triggers ctrl+c, but only when the
// codepoint is not itself a Latin letter or known symbol: on remapped layouts
// (Dvorak, Colemak) the codepoint is authoritative, so Dvorak Ctrl+K (base v)
// never matches ctrl+v.
func matchesKittySequence(pk *parsedKey, expectedCodepoint, expectedModifier int) bool {
	if pk.modifier != expectedModifier&^lockMask {
		return false
	}
	normalized := normalizeShiftedLetterIdentityCodepoint(normalizeKittyFunctionalCodepoint(pk.codepoint), pk.modifier)
	if normalized == normalizeShiftedLetterIdentityCodepoint(normalizeKittyFunctionalCodepoint(expectedCodepoint), expectedModifier) {
		return true
	}
	if pk.hasBaseLayoutKey && pk.baseLayoutKey == expectedCodepoint {
		isLatinLetter := normalized >= 'a' && normalized <= 'z'
		return !isLatinLetter && !isSymbolCodepoint(normalized)
	}
	return false
}

// parseModifyOtherKeys parses xterm modifyOtherKeys: \x1b[27;<modifier>;<codepoint>~
func parseModifyOtherKeys(data string) *parsedKey {
	if len(data) < 9 || data[0] != '\x1b' || data[1] != '[' || data[len(data)-1] != '~' {
		return nil
	}
	body := data[2 : len(data)-1]
	if !strings.HasPrefix(body, "27;") {
		return nil
	}
	rest := body[3:] // after "27;"
	parts := strings.SplitN(rest, ";", 2)
	if len(parts) != 2 {
		return nil
	}
	modVal, err1 := strconv.Atoi(parts[0])
	cp, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return nil
	}
	return &parsedKey{codepoint: cp, modifier: (modVal - 1) & ^lockMask}
}

// parseModifiedArrow parses modified arrow key: \x1b[1;<modifier><A-D>
// Also handles \x1b[1;<modifier>:<event><A-D> (Kitty flag 2).
func parseModifiedArrow(data string) *parsedKey {
	if len(data) < 6 || data[0] != '\x1b' || data[1] != '[' || data[2] != '1' || data[3] != ';' {
		return nil
	}
	final := data[len(data)-1]
	cp, ok := arrowFinals[final]
	if !ok {
		// Also check H/F for home/end
		cp2, ok2 := homeEndFinals[final]
		if !ok2 {
			return nil
		}
		cp = cp2
	}
	modStr := data[4 : len(data)-1]
	// Strip event type: mod:event → mod
	if idx := strings.IndexByte(modStr, ':'); idx >= 0 {
		modStr = modStr[:idx]
	}
	modVal, err := strconv.Atoi(modStr)
	if err != nil {
		return nil
	}
	return &parsedKey{codepoint: cp, modifier: (modVal - 1) & ^lockMask}
}

// parseModifiedFunc parses a Kitty functional key sequence with an optional
// modifier and event type: \x1b[<n>[;<modifier>][:<event>]~.
func parseModifiedFunc(data string) *parsedKey {
	if len(data) < 4 || data[0] != '\x1b' || data[1] != '[' || data[len(data)-1] != '~' {
		return nil
	}
	body := data[2 : len(data)-1]
	params, event, hasEvent := strings.Cut(body, ":")
	if hasEvent {
		if event == "" {
			return nil
		}
		if _, err := strconv.Atoi(event); err != nil {
			return nil
		}
	}

	numText := params
	modValue := 1
	if before, after, hasModifier := strings.Cut(params, ";"); hasModifier {
		numText = before
		parsed, err := strconv.Atoi(after)
		if err != nil {
			return nil
		}
		modValue = parsed
	}
	num, err := strconv.Atoi(numText)
	if err != nil {
		return nil
	}
	cp, ok := funcKeyCodes[num]
	if !ok {
		return nil
	}
	return &parsedKey{codepoint: cp, modifier: (modValue - 1) & ^lockMask}
}

// parseTerminalInput attempts to parse raw terminal data into a (codepoint, modifier) pair
// by trying the non-Kitty CSI formats (Kitty CSI-u goes through
// matchesKittySequence).
func parseTerminalInput(data string) *parsedKey {
	if pk := parseModifyOtherKeys(data); pk != nil {
		if pk.modifier&modShift != 0 && pk.codepoint >= 65 && pk.codepoint <= 90 {
			pk.codepoint += 32
		}
		return pk
	}
	if pk := parseModifiedArrow(data); pk != nil {
		return pk
	}
	if pk := parseModifiedFunc(data); pk != nil {
		return pk
	}
	return nil
}

// parseKeyID parses a key identifier string like "ctrl+shift+left" into
// (baseKey, modifier bitmask).
func parseKeyID(keyID string) (baseKey string, modifier int, ok bool) {
	parts := strings.Split(strings.ToLower(keyID), "+")
	baseKey = parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			modifier |= modCtrl
		case "shift":
			modifier |= modShift
		case "alt":
			modifier |= modAlt
		case "super":
			modifier |= modSuper
		default:
			return "", 0, false
		}
	}
	return baseKey, modifier, true
}

// resolveKeyCodepoint converts a base key name to its codepoint.
// Returns the codepoint and true if resolved, 0 and false otherwise.
func resolveKeyCodepoint(baseKey string) (cp int, ok bool) {
	// Check named keys first.
	if entry, found := keyNameCodepoints[baseKey]; found {
		return entry.cp, true
	}
	// Single printable character → its codepoint.
	if len(baseKey) == 1 {
		ch := baseKey[0]
		if ch >= 'a' && ch <= 'z' {
			return int(ch), true
		}
		if ch >= '0' && ch <= '9' {
			return int(ch), true
		}
		// Symbol keys
		if isSymbolKey(ch) {
			return int(ch), true
		}
	}
	// Function keys f1-f12: not CSI-u, legacy-only. Not handled dynamically.
	return 0, false
}

func isSymbolKey(ch byte) bool {
	return strings.IndexByte("`-=[]\\;',./!@#$%^&*()_+|~{}:<>?", ch) >= 0
}

// rawCtrlChar returns the control character for a key (code & 0x1f).
// Returns 0 if no control character mapping exists.
func rawCtrlChar(key byte) byte {
	if key >= 'a' && key <= 'z' {
		return key & 0x1f
	}
	switch key {
	case '[', '\\', ']', '_':
		return key & 0x1f
	case '-':
		return 31 // Same as Ctrl+_
	}
	return 0
}

// matchesKeyDynamic checks if raw terminal input matches a KeyID
// by dynamically computing expected codepoints and modifier bits.
//
// It handles all three protocols: CSI-u, modifyOtherKeys, legacy CSI.
func matchesKeyDynamic(data, keyID string) bool {
	baseKey, expectedMod, ok := parseKeyID(keyID)
	if !ok {
		return false
	}

	expectedCP, cpOK := resolveKeyCodepoint(baseKey)
	if !cpOK {
		return false
	}

	// Kitty CSI-u, including flag-4 alternate keys. Enter also answers to
	// the keypad Enter codepoint.
	if pk := parseCSIu(data); pk != nil {
		if matchesKittySequence(pk, expectedCP, expectedMod) {
			return true
		}
		return expectedCP == cpEnter && matchesKittySequence(pk, cpKPEnter, expectedMod)
	}

	// Parse the raw terminal input.
	pk := parseTerminalInput(data)
	if pk != nil {
		return pk.codepoint == expectedCP && pk.modifier == expectedMod
	}

	// Fallback: legacy single-byte / short-sequence matching.
	return matchesLegacy(data, baseKey, expectedCP, expectedMod)
}

// legacyPlain lists each navigation key's unmodified legacy sequences.
var legacyPlain = map[string][]string{
	"delete":   {"\x1b[3~"},
	"insert":   {"\x1b[2~"},
	"home":     {"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~"},
	"end":      {"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~"},
	"pageup":   {"\x1b[5~", "\x1b[[5~"},
	"pagedown": {"\x1b[6~", "\x1b[[6~"},
	"up":       {"\x1b[A", "\x1bOA"},
	"down":     {"\x1b[B", "\x1bOB"},
	"left":     {"\x1b[D", "\x1bOD"},
	"right":    {"\x1b[C", "\x1bOC"},
}

// legacyAlt lists the arrows' alt sequences; legacyAltNoKitty adds the ones
// only valid outside the Kitty protocol.
var (
	legacyAlt = map[string][]string{
		"up":    {"\x1bp", "\x1b[1;3A"},
		"down":  {"\x1bn", "\x1b[1;3B"},
		"left":  {"\x1bb", "\x1b[1;3D"},
		"right": {"\x1bf", "\x1b[1;3C"},
	}
	legacyAltNoKitty = map[string]string{"left": "\x1bB", "right": "\x1bF"}
)

// rxvtSeq holds each navigation key's rxvt {shift, ctrl} sequences.
var rxvtSeq = map[string][2]string{
	"up":       {"\x1b[a", "\x1bOa"},
	"down":     {"\x1b[b", "\x1bOb"},
	"right":    {"\x1b[c", "\x1bOc"},
	"left":     {"\x1b[d", "\x1bOd"},
	"insert":   {"\x1b[2$", "\x1b[2^"},
	"delete":   {"\x1b[3$", "\x1b[3^"},
	"pageup":   {"\x1b[5$", "\x1b[5^"},
	"pagedown": {"\x1b[6$", "\x1b[6^"},
	"home":     {"\x1b[7$", "\x1b[7^"},
	"end":      {"\x1b[8$", "\x1b[8^"},
}

// matchesLegacy handles pre-CSI-u terminal sequences.
func matchesLegacy(data, baseKey string, expectedCP, expectedMod int) bool {
	// Special named keys with legacy sequences.
	switch baseKey {
	case "escape", "esc":
		return expectedMod == 0 && data == "\x1b"

	case "enter", "return":
		if expectedMod == 0 {
			return data == "\r" || !IsKittyProtocolActive() && data == "\n" || data == "\x1bOM"
		}
		if expectedMod == modShift {
			return IsKittyProtocolActive() && (data == "\x1b\r" || data == "\n")
		}
		if expectedMod == modAlt {
			return !IsKittyProtocolActive() && data == "\x1b\r"
		}
		return false

	case "tab":
		if expectedMod == 0 {
			return data == "\t"
		}
		if expectedMod == modShift {
			return data == "\x1b[Z"
		}
		return false

	case "space":
		if expectedMod == 0 {
			return data == " "
		}
		if expectedMod == modCtrl {
			return !IsKittyProtocolActive() && data == "\x00"
		}
		if expectedMod == modAlt {
			return !IsKittyProtocolActive() && data == "\x1b "
		}
		return false

	case "backspace":
		if expectedMod == 0 || expectedMod == modCtrl {
			return matchesRawBackspace(data, expectedMod)
		}
		if expectedMod == modAlt {
			return data == "\x1b\x7f" || data == "\x1b\x08"
		}
		return false
	}

	// Navigation keys: fixed unmodified (and alt) sequences, else the
	// xterm/rxvt modified forms.
	if plain, ok := legacyPlain[baseKey]; ok {
		if expectedMod == 0 {
			return slices.Contains(plain, data)
		}
		if alt, ok := legacyAlt[baseKey]; ok && expectedMod == modAlt {
			if seq, ok := legacyAltNoKitty[baseKey]; ok && !IsKittyProtocolActive() && data == seq {
				return true
			}
			return slices.Contains(alt, data)
		}
		return matchesLegacyModSeq(data, baseKey, expectedMod)
	}

	// Single character keys (letters, digits, symbols).
	if len(baseKey) == 1 {
		ch := baseKey[0]

		if expectedMod == 0 {
			return len(data) == 1 && data[0] == ch
		}

		// Ctrl+letter → control character
		if expectedMod == modCtrl {
			if rc := rawCtrlChar(ch); rc != 0 {
				return len(data) == 1 && data[0] == rc
			}
		}

		// Alt+letter → ESC followed by letter in legacy mode only.
		if expectedMod == modAlt && !IsKittyProtocolActive() && len(data) == 2 && data[0] == '\x1b' {
			return data[1] == ch
		}

		// Ctrl+Alt+letter → ESC followed by a control character in legacy mode only.
		if expectedMod == modCtrl|modAlt && !IsKittyProtocolActive() {
			if rc := rawCtrlChar(ch); rc != 0 {
				return len(data) == 2 && data[0] == '\x1b' && data[1] == rc
			}
		}

		// Shift+letter → uppercase
		if expectedMod == modShift && ch >= 'a' && ch <= 'z' {
			return len(data) == 1 && data[0] == ch-32
		}
	}

	return false
}

// matchesLegacyModSeq checks legacy modified sequences for navigation keys.
// Handles both xterm-style (\x1b[1;<mod>D) and rxvt-style (\x1b[d, \x1bOd).
func matchesLegacyModSeq(data, baseKey string, mod int) bool {
	// xterm-style modified arrows: \x1b[1;<mod+1><final>
	if entry, ok := keyNameCodepoints[baseKey]; ok {
		modStr := strconv.Itoa(mod + 1)
		if entry.isArrow {
			if final, ok := arrowCPtoFinal[entry.cp]; ok {
				expected := fmt.Sprintf("\x1b[1;%s%c", modStr, final)
				if data == expected {
					return true
				}
			}
		} else if final, ok := homeCPtoFinal[entry.cp]; ok {
			// home/end: \x1b[1;<mod>H/F
			expected := fmt.Sprintf("\x1b[1;%s%c", modStr, final)
			if data == expected {
				return true
			}
		}
		if num, ok := funcCPtoNum[entry.cp]; ok {
			// Functional: \x1b[<num>;<mod>~
			expected := fmt.Sprintf("\x1b[%d;%s~", num, modStr)
			if data == expected {
				return true
			}
		}
	}

	if seqs, ok := rxvtSeq[baseKey]; ok {
		switch mod {
		case modShift:
			return data == seqs[0]
		case modCtrl:
			return data == seqs[1]
		}
	}
	return false
}
