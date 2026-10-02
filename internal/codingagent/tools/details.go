package tools

// EditToolDetails is attached to an EditTool result. It is the extension
// SDK contract: a display-oriented diff,
// a standard unified patch, and the new-file line of the first change
// (for editor navigation). The TUI renders Diff directly; extensions and
// PostToolUse hooks receive this exact JSON shape.
type EditToolDetails struct {
	// Diff is the display-oriented, line-numbered diff (generateDiffString).
	Diff string `json:"diff"`
	// Patch is the standard unified patch (generateUnifiedPatch).
	Patch string `json:"patch"`
	// FirstChangedLine is the new-file line of the first change, 0 when
	// there is no change (omitted).
	FirstChangedLine int `json:"firstChangedLine,omitempty"`
	// Loose says how edits matched that weren't found exactly, e.g.
	// "edits[2] matched ignoring indentation".
	Loose []string `json:"loose,omitempty"`
}

// WriteDetails is attached to a WriteTool result. The TUI uses the path to
// select syntax highlighting and the content to render the ten-line
// collapsed preview or complete expanded body.
type WriteDetails struct {
	Path    string
	Content string
	// Overwrote is true when the write replaced an existing file.
	Overwrote bool
}

// ReadDetails is attached to a ReadTool result. The TUI uses the path for
// syntax highlighting and the truncation details for the warning shown after
// the complete expanded body.
type ReadDetails struct {
	Path       string
	StartLine  int // 1-indexed line number of the first emitted line
	TotalLines int // total lines in the file (before truncation)
	Truncated  bool
	// Truncation is the truncation object for the extension SDK
	// contract and the TUI warning. It is nil unless truncation occurred.
	Truncation *TruncationResult
}

// BashDetails is attached to a BashTool result. The renderer reads
// Truncation to format the warning row
// (e.g. `[Showing lines 1001-3000 of 3000. Full output: /tmp/...]`)
// and FullOutputPath to surface the temp file containing the full
// (untruncated) output.
//
// Either field may be zero/nil; both nil means the bash output fit
// fully in the rolling buffer.
type BashDetails struct {
	Truncation     *TruncationResult
	FullOutputPath string
	// Changes are the files the command changed, with their diffs (for the
	// card; not sent to the model).
	Changes []ShellFileChange `json:"changes,omitempty"`
}

// MaxShellDiffBytes caps the diff a bash card keeps for one file; a larger
// one is dropped (the card shows the file changed, without its diff).
const MaxShellDiffBytes = 256 << 10

// ShellFileChange is one file a shell command changed.
type ShellFileChange struct {
	Path string `json:"path"`
	// Kind is "edited", "created", or "deleted".
	Kind string `json:"kind"`
	// Diff is the display diff (GenerateDiffString); empty for a file too
	// large to diff.
	Diff string `json:"diff,omitempty"`
}

// LsDetails is attached to a LsTool result. It carries the extension SDK
// contract: a truncation object when the byte
// limit was hit and the entry-limit cap when reached. The TUI has no
// dedicated ls renderer; this exists only for the extension wire.
type LsDetails struct {
	Truncation        *TruncationResult
	EntryLimitReached int
}

// GrepDetails is attached to a GrepTool result: a truncation
// object when the byte limit was hit, the match-limit cap when reached,
// and a flag when individual lines were truncated to the max length.
type GrepDetails struct {
	Truncation        *TruncationResult
	MatchLimitReached int
	LinesTruncated    bool
}

// FindDetails is attached to a FindTool result: a truncation
// object when the byte limit was hit and the result-limit cap when reached.
type FindDetails struct {
	Truncation         *TruncationResult
	ResultLimitReached int
}
