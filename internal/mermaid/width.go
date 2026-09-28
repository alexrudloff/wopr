// SPDX-FileCopyrightText: Copyright Hewlett Packard Enterprise Development LP
// SPDX-FileCopyrightText: Copyright 2023-2026 SpaceXAI
// SPDX-FileCopyrightText: Copyright 2026 Alexey Zaytsev
// SPDX-License-Identifier: Apache-2.0 AND MIT

package mermaid

import "github.com/alexrudloff/wopr/tui/widthx"

// Display width, measured in grapheme clusters with the same widths the TUI
// paints (tui/widthx), so a box is sized for exactly what gets drawn.

// stringWidth returns the display columns of a string.
func stringWidth(s string) int { return widthx.VisibleWidth(s) }

// measuredCluster pairs a cluster with its display width.
type measuredCluster struct {
	cluster string
	width   int
}

func measured(s string) []measuredCluster {
	var out []measuredCluster
	for s != "" {
		var seg string
		seg, s = widthx.FirstGrapheme(s)
		out = append(out, measuredCluster{cluster: seg, width: widthx.GraphemeWidth(seg)})
	}
	return out
}
