package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Blocks spinner geometry: an 8-cell scanner whose lit head sweeps right,
// holds, sweeps back, and holds, trailing a fading tail.
const (
	blocksWidth     = 8
	blocksHoldStart = 30
	blocksHoldEnd   = 9
	blocksTrail     = 6
	blocksInactive  = 0.6
	blocksMinAlpha  = 0.3
	// BlocksSpinnerIntervalMs is the frame period of the blocks spinner.
	BlocksSpinnerIntervalMs = 40
)

// BlocksSpinnerFrames is the number of frames in one blocks spinner cycle.
const BlocksSpinnerFrames = blocksWidth + blocksHoldEnd + (blocksWidth - 1) + blocksHoldStart

// BlocksSpinner renders frame f of the blocks spinner in colorHex, blended
// over bgHex: lit cells are "■", the rest "⬝".
func BlocksSpinner(frame int, colorHex, bgHex string) string {
	f := ((frame % BlocksSpinnerFrames) + BlocksSpinnerFrames) % BlocksSpinnerFrames
	var active int
	forward, holding := true, false
	var holdProgress, holdTotal, moveProgress, moveTotal int
	switch {
	case f < blocksWidth:
		active, moveProgress, moveTotal = f, f, blocksWidth
	case f < blocksWidth+blocksHoldEnd:
		active, holding, holdProgress, holdTotal = blocksWidth-1, true, f-blocksWidth, blocksHoldEnd
	case f < blocksWidth+blocksHoldEnd+blocksWidth-1:
		step := f - blocksWidth - blocksHoldEnd
		active, forward, moveProgress, moveTotal = blocksWidth-2-step, false, step, blocksWidth-1
	default:
		active, forward, holding = 0, false, true
		holdProgress, holdTotal = f-blocksWidth-blocksHoldEnd-(blocksWidth-1), blocksHoldStart
	}
	fade := blocksMinAlpha + float64(moveProgress)/float64(max(1, moveTotal-1))*(1-blocksMinAlpha)
	if holding {
		fade = math.Max(blocksMinAlpha, 1-float64(holdProgress)/float64(holdTotal)*(1-blocksMinAlpha))
	}
	var b strings.Builder
	for i := range blocksWidth {
		d := active - i
		if !forward {
			d = i - active
		}
		idx := -1
		switch {
		case holding:
			idx = d + holdProgress
		case d == 0:
			idx = 0
		case d > 0 && d < blocksTrail:
			idx = d
		}
		glyph, alpha, boost := "⬝", blocksInactive*fade, 1.0
		if idx >= 0 && idx < blocksTrail {
			glyph = "■"
			switch idx {
			case 0:
				alpha = 1
			case 1:
				alpha, boost = 0.9, 1.15
			default:
				alpha = math.Pow(0.65, float64(idx-1))
			}
		}
		b.WriteString(ThemeHexFg(BlendHex(bgHex, ScaleHex(colorHex, boost), alpha)))
		b.WriteString(glyph)
	}
	b.WriteString(SGRFgReset)
	return b.String()
}

// BlendHex mixes over into base by alpha (0 keeps base, 1 gives over).
// Unparseable input returns over unchanged.
func BlendHex(base, over string, alpha float64) string {
	br, bg, bb, ok1 := parseHexRGB(base)
	or, og, ob, ok2 := parseHexRGB(over)
	if !ok1 || !ok2 {
		return over
	}
	mix := func(a, b int) int { return int(math.Round(float64(a) + (float64(b)-float64(a))*alpha)) }
	return fmt.Sprintf("#%02x%02x%02x", mix(br, or), mix(bg, og), mix(bb, ob))
}

// ScaleHex multiplies each channel by factor, clamped to 255.
func ScaleHex(hex string, factor float64) string {
	r, g, b, ok := parseHexRGB(hex)
	if !ok || factor == 1 {
		return hex
	}
	scale := func(c int) int { return min(255, int(math.Round(float64(c)*factor))) }
	return fmt.Sprintf("#%02x%02x%02x", scale(r), scale(g), scale(b))
}

func parseHexRGB(hex string) (r, g, b int, ok bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}
