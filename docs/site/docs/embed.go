// Package sitedocs embeds the user documentation pages so the wopr binary can
// ship them. These Markdown files are the single documentation source: the
// site renders them, and internal/woprdocs materializes them under
// ~/.wopr/docs for the running agent.
package sitedocs

import "embed"

// FS holds every documentation page at its root.
//
//go:embed *.md
var FS embed.FS
