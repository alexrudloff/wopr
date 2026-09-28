// Tail/head truncation helpers for tool outputs.
//
// Limits:
//
//   - DEFAULT_MAX_BYTES = 50 KB
//   - DEFAULT_MAX_LINES = 2000
//   - GREP_MAX_LINE_LENGTH = 500 chars per match line
//
// truncateTail keeps the LAST N lines/bytes (bash output: errors and
// final results live there). truncateHead keeps the FIRST N (file
// reads: beginning matters).
//
// "Whichever is hit first" applies to both: line cap or byte cap.
//
// For the bash tail-truncation edge case where the LAST line alone
// exceeds maxBytes, we return that line truncated from its end with
// LastLinePartial = true so the renderer can show
// "[Showing last <bytes> of line N (line is <bytes>). ...]"

package tools

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// splitLinesForCounting splits content into lines for truncation counting,
// treating a trailing newline as a line terminator rather than an empty
// final line.
func splitLinesForCounting(content string) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncationResult captures every fact a renderer needs to format the
// "[Showing lines X-Y of Z. Full output: <path>]" warning row.
type TruncationResult struct {
	Content     string
	Truncated   bool
	TruncatedBy string // "lines" | "bytes" | ""
	TotalLines  int
	TotalBytes  int
	OutputLines int
	OutputBytes int
	// LastLinePartial: the last line of the original output is the
	// only one that fit (and was truncated from its end). Bash-only
	// edge case.
	LastLinePartial bool
	// FirstLineExceedsLimit: head-truncation case where the first line
	// alone exceeds maxBytes. We return empty content and let the
	// caller decide.
	FirstLineExceedsLimit bool
	MaxLines              int
	MaxBytes              int
}

// TruncateTail keeps the last lines/bytes of content. Used for bash
// output (errors and final results live at the end).
//
// maxBytes and maxLines are used as given; callers pass the defaults.
func TruncateTail(content string, maxBytes, maxLines int) TruncationResult {
	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	// Walk backwards collecting lines that still fit.
	var collected []string
	outputBytes := 0
	truncatedBy := "lines"
	lastLinePartial := false
	for i := len(lines) - 1; i >= 0 && len(collected) < maxLines; i-- {
		line := lines[i]
		// +1 for the joining newline, except for the very first line
		// added (which has no preceding newline in the joined output).
		extra := 0
		if len(collected) > 0 {
			extra = 1
		}
		lineBytes := len(line) + extra
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = "bytes"
			if len(collected) == 0 {
				// Edge: this single line is bigger than maxBytes.
				// Take its END (last `maxBytes` bytes), respecting
				// UTF-8 boundaries.
				partial := tailNBytesUTF8(line, maxBytes)
				collected = append(collected, partial)
				outputBytes = len(partial)
				lastLinePartial = true
			}
			break
		}
		// Prepend (we're walking backwards).
		collected = append([]string{line}, collected...)
		outputBytes += lineBytes
	}
	if len(collected) >= maxLines && outputBytes <= maxBytes {
		truncatedBy = "lines"
	}
	out := strings.Join(collected, "\n")
	return TruncationResult{
		Content:         out,
		Truncated:       true,
		TruncatedBy:     truncatedBy,
		TotalLines:      totalLines,
		TotalBytes:      totalBytes,
		OutputLines:     len(collected),
		OutputBytes:     len(out),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines,
		MaxBytes:        maxBytes,
	}
}

// TruncateHead keeps the FIRST lines/bytes of content. Used for grep
// output, find results, etc.: anywhere we want to see the
// beginning.
//
// Never returns partial lines: if the first line alone exceeds
// maxBytes, the result is empty content with `FirstLineExceedsLimit`
// set so the caller can emit the `[First line exceeds N
// limit]` warning.
func TruncateHead(content string, maxBytes, maxLines int) TruncationResult {
	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	// First-line-exceeds-limit edge.
	if len(lines) > 0 && len(lines[0]) > maxBytes {
		return TruncationResult{
			Content:               "",
			Truncated:             true,
			TruncatedBy:           "bytes",
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           0,
			OutputBytes:           0,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	var collected []string
	outputBytes := 0
	truncatedBy := "lines"
	for i := 0; i < len(lines) && len(collected) < maxLines; i++ {
		line := lines[i]
		extra := 0
		if i > 0 {
			extra = 1 // joining newline
		}
		lineBytes := len(line) + extra
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}
		collected = append(collected, line)
		outputBytes += lineBytes
	}
	if len(collected) >= maxLines && outputBytes <= maxBytes {
		truncatedBy = "lines"
	}
	out := strings.Join(collected, "\n")
	return TruncationResult{
		Content:     out,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(collected),
		OutputBytes: len(out),
		MaxLines:    maxLines,
		MaxBytes:    maxBytes,
	}
}

// FormatTruncationWarning returns the `[Truncated: ...]` warning string
// for a TruncationResult, or "" if not truncated. Producers that don't
// have a separate render layer (wopr grep / find / etc.) append
// the warning directly to the LLM-visible content.
func FormatTruncationWarning(tr TruncationResult) string {
	if !tr.Truncated {
		return ""
	}
	if tr.FirstLineExceedsLimit {
		return "[First line exceeds " + FormatSize(tr.MaxBytes) + " limit]"
	}
	if tr.TruncatedBy == "lines" {
		return "[Truncated: showing " + strconv.Itoa(tr.OutputLines) + " of " + strconv.Itoa(tr.TotalLines) + " lines (" + strconv.Itoa(tr.MaxLines) + " line limit)]"
	}
	return "[Truncated: " + strconv.Itoa(tr.OutputLines) + " lines shown (" + FormatSize(tr.MaxBytes) + " limit)]"
}

// tailNBytesUTF8 returns the last n bytes of s, advanced forward to
// the next UTF-8 character boundary so we don't return invalid runes.
func tailNBytesUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// FormatSize renders a byte count as "123B" / "12.3KB" / "1.2MB".
func FormatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return strconv.Itoa(bytes) + "B"
	case bytes < 1024*1024:
		return fmtFloat(float64(bytes)/1024.0, "KB")
	default:
		return fmtFloat(float64(bytes)/(1024.0*1024.0), "MB")
	}
}

func fmtFloat(f float64, suffix string) string {
	// One decimal, rounded half up.
	scaled := int64(f*10 + 0.5)
	whole := scaled / 10
	frac := scaled % 10
	return strconv.Itoa(int(whole)) + "." + string('0'+byte(frac)) + suffix
}

// TruncateLine cuts a line longer than maxChars
// UTF-16 code units (JavaScript string length) is cut to maxChars units plus
// "... [truncated]". A cut inside a surrogate pair leaves U+FFFD, as the
// lone surrogate JavaScript would keep serializes to.
func TruncateLine(line string, maxChars int) (string, bool) {
	if jsLength(line) <= maxChars {
		return line, false
	}
	var b strings.Builder
	units := 0
	for _, r := range line {
		n := utf16.RuneLen(r)
		if units+n > maxChars {
			if units < maxChars {
				b.WriteRune(utf8.RuneError)
			}
			break
		}
		b.WriteRune(r)
		units += n
	}
	return b.String() + "... [truncated]", true
}
