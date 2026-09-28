package codingagent

import (
	"bytes"
	"cmp"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexrudloff/wopr/tui"
)

const (
	escByte                = "\x1b"
	bracketedPasteStart    = "\x1b[200~"
	bracketedPasteEnd      = "\x1b[201~"
	defaultSequenceTimeout = 50 * time.Millisecond
	defaultEscapeTimeout   = 10 * time.Millisecond
)

// StdinBuffer accumulates partial escape sequences across reads so the input loop
// only sees complete chunks. Bracketed pastes are re-emitted as a single
// framed payload because dispatchKey/classifyKey currently consume the
// ESC[200~...ESC[201~ form directly. The zero value uses the default
// timeouts.
type StdinBuffer struct {
	buffer         string
	pasteMode      bool
	pasteBuffer    string
	pendingKittyCP int
	timeout        time.Duration
	escapeTimeout  time.Duration
	// utf8Pending holds the leading bytes of a character split across reads.
	utf8Pending []byte
}

// StdinBufferOptions configures a StdinBuffer. A zero field keeps the default.
type StdinBufferOptions struct {
	// Timeout is how long an incomplete escape sequence waits for more input.
	Timeout time.Duration
	// EscapeTimeout is how long a lone ESC waits before it is the Escape key.
	EscapeTimeout time.Duration
}

// NewStdinBuffer returns a StdinBuffer with options applied.
func NewStdinBuffer(options StdinBufferOptions) *StdinBuffer {
	return &StdinBuffer{timeout: options.Timeout, escapeTimeout: options.EscapeTimeout}
}

// newProcessStdinBuffer builds the buffer terminal input loops use, with
// resolveEscapeTimeoutMs() as the escape timeout.
func newProcessStdinBuffer() *StdinBuffer {
	ms := tui.ResolveEscapeTimeoutMs(os.Getenv)
	return NewStdinBuffer(StdinBufferOptions{EscapeTimeout: time.Duration(ms * float64(time.Millisecond))})
}

// FlushTimeout is how long the buffered remainder waits for more input before
// Flush emits it: the escape timeout for a lone ESC, the sequence timeout for
// anything else.
func (b *StdinBuffer) FlushTimeout() time.Duration {
	if b.buffer == escByte {
		return cmp.Or(b.escapeTimeout, defaultEscapeTimeout)
	}
	return cmp.Or(b.timeout, defaultSequenceTimeout)
}

// ProcessTerminalBytes feeds one terminal read. It decodes UTF-8 across reads
// so a
// character split between reads is held until it is complete and a lone high
// byte is never rewritten as a Meta key; the decoded text then goes through
// ProcessString.
func (b *StdinBuffer) ProcessTerminalBytes(data []byte) []string {
	if len(b.utf8Pending) > 0 {
		data = append(b.utf8Pending, data...)
		b.utf8Pending = nil
	}
	if cut := incompleteUTF8Suffix(data); cut > 0 {
		b.utf8Pending = bytes.Clone(data[len(data)-cut:])
		data = data[:len(data)-cut]
		if len(data) == 0 {
			return nil
		}
	}
	return b.ProcessString(string(data))
}

// stdinFlushTimer is the flush timeout a terminal input loop arms while its
// StdinBuffer holds an incomplete sequence. The loop selects on C; after a
// receive it calls stop and then Flush.
type stdinFlushTimer struct {
	timer *time.Timer
	C     <-chan time.Time
}

// sync re-arms the timer for b's pending remainder, or stops it when nothing
// is pending: the timeout clears on every call and is rescheduled only while
// the buffer is non-empty.
func (f *stdinFlushTimer) sync(b *StdinBuffer) {
	f.stop()
	if b.HasPendingFlush() {
		f.timer = time.NewTimer(b.FlushTimeout())
		f.C = f.timer.C
	}
}

func (f *stdinFlushTimer) stop() {
	if f.timer != nil {
		f.timer.Stop()
	}
	f.timer = nil
	f.C = nil
}

// incompleteUTF8Suffix returns the length of a trailing UTF-8 sequence that is
// still missing continuation bytes, or 0.
func incompleteUTF8Suffix(data []byte) int {
	for back := 1; back <= utf8.UTFMax-1 && back <= len(data); back++ {
		c := data[len(data)-back]
		if c < utf8.RuneSelf {
			return 0
		}
		if utf8.RuneStart(c) {
			if utf8.FullRune(data[len(data)-back:]) {
				return 0
			}
			return back
		}
	}
	return 0
}

// ProcessString feeds s and returns any complete keystroke chunks.
func (b *StdinBuffer) ProcessString(s string) []string {
	if s == "" {
		if b.buffer == "" {
			return b.emitDataSequence(nil, "")
		}
		return nil
	}
	b.buffer += s

	if b.pasteMode {
		b.pasteBuffer += b.buffer
		b.buffer = ""
		if end := strings.Index(b.pasteBuffer, bracketedPasteEnd); end >= 0 {
			payload := b.pasteBuffer[:end]
			remaining := b.pasteBuffer[end+len(bracketedPasteEnd):]
			b.pasteMode = false
			b.pasteBuffer = ""
			b.pendingKittyCP = 0
			out := []string{bracketedPasteStart + payload + bracketedPasteEnd}
			if remaining != "" {
				out = append(out, b.ProcessString(remaining)...)
			}
			return out
		}
		return nil
	}

	if start := strings.Index(b.buffer, bracketedPasteStart); start >= 0 {
		var out []string
		if start > 0 {
			result, remainder := extractCompleteSequences(b.buffer[:start])
			out = b.emitDataSequence(out, result...)
			if remainder != "" {
				out = b.emitDataSequence(out, remainder)
			}
		}
		b.pendingKittyCP = 0
		b.buffer = b.buffer[start+len(bracketedPasteStart):]
		b.pasteMode = true
		b.pasteBuffer = b.buffer
		b.buffer = ""
		if end := strings.Index(b.pasteBuffer, bracketedPasteEnd); end >= 0 {
			payload := b.pasteBuffer[:end]
			remaining := b.pasteBuffer[end+len(bracketedPasteEnd):]
			b.pasteMode = false
			b.pasteBuffer = ""
			b.pendingKittyCP = 0
			out = append(out, bracketedPasteStart+payload+bracketedPasteEnd)
			if remaining != "" {
				out = append(out, b.ProcessString(remaining)...)
			}
		}
		return out
	}

	result, remainder := extractCompleteSequences(b.buffer)
	b.buffer = remainder
	return b.emitDataSequence(nil, result...)
}

// Flush emits any incomplete remainder as a single chunk. It is the
// timeout-based fallback path; the input loop owns the timer.
func (b *StdinBuffer) Flush() []string {
	if b.buffer == "" {
		b.pendingKittyCP = 0
		return nil
	}
	out := b.emitDataSequence(nil, b.buffer)
	b.buffer = ""
	b.pendingKittyCP = 0
	return out
}

func (b *StdinBuffer) HasPendingFlush() bool {
	return b.buffer != ""
}

func parseUnmodifiedKittyPrintableCodepoint(sequence string) (int, bool) {
	if !strings.HasPrefix(sequence, escByte+"[") || !strings.HasSuffix(sequence, "u") {
		return 0, false
	}
	payload := sequence[2 : len(sequence)-1]
	if payload == "" {
		return 0, false
	}
	parts := strings.Split(payload, ":")
	head := parts[0]
	if head == "" {
		return 0, false
	}
	semi := strings.Split(head, ";")
	if len(semi) != 1 {
		return 0, false
	}
	cp, err := strconv.Atoi(semi[0])
	if err != nil || cp < 32 {
		return 0, false
	}
	if len(parts) > 1 && parts[1] != "" {
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return 0, false
		}
	}
	if len(parts) > 2 {
		mods := strings.Split(parts[2], ";")
		if len(mods) > 2 {
			return 0, false
		}
		for _, p := range mods {
			if p == "" {
				continue
			}
			if _, err := strconv.Atoi(p); err != nil {
				return 0, false
			}
		}
	}
	if len(parts) > 3 {
		return 0, false
	}
	return cp, true
}

func (b *StdinBuffer) emitDataSequence(out []string, sequences ...string) []string {
	for _, sequence := range sequences {
		if rawCodepoint := singleRuneCodepoint(sequence); rawCodepoint != 0 && rawCodepoint == b.pendingKittyCP {
			b.pendingKittyCP = 0
			continue
		}
		if cp, ok := parseUnmodifiedKittyPrintableCodepoint(sequence); ok {
			b.pendingKittyCP = cp
		} else {
			b.pendingKittyCP = 0
		}
		out = append(out, sequence)
	}
	return out
}

func singleRuneCodepoint(s string) int {
	if s == "" {
		return 0
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		return 0
	}
	if size != len(s) {
		return 0
	}
	return int(r)
}

func isCompleteSequence(data string) string {
	if !strings.HasPrefix(data, escByte) {
		return "not-escape"
	}
	if len(data) == 1 {
		return "incomplete"
	}
	afterEsc := data[1:]
	switch {
	case strings.HasPrefix(afterEsc, "[M"):
		// X10 mouse reports are ESC[M followed by three encoded bytes. The M
		// introduces the payload; unlike an ordinary CSI final byte it does
		// not complete the sequence by itself.
		if len(data) >= 6 {
			return "complete"
		}
		return "incomplete"
	case strings.HasPrefix(afterEsc, "["):
		return isCompleteCSISequence(data)
	case strings.HasPrefix(afterEsc, "]"):
		return stringSequenceStatus(data, true)
	case strings.HasPrefix(afterEsc, "P"), strings.HasPrefix(afterEsc, "_"):
		return stringSequenceStatus(data, false)
	case strings.HasPrefix(afterEsc, "O"):
		if len(afterEsc) >= 2 {
			return "complete"
		}
		return "incomplete"
	default:
		return "complete"
	}
}

// isCompleteCSISequence reports on an ESC [ sequence: it completes at a final
// byte, and an SGR mouse report only once its three fields are whole.
func isCompleteCSISequence(data string) string {
	if len(data) < 3 {
		return "incomplete"
	}
	payload := data[2:]
	lastChar := payload[len(payload)-1]
	if lastChar < 0x40 || lastChar > 0x7e || strings.HasPrefix(payload, "<") && !sgrMousePayload(payload) {
		return "incomplete"
	}
	return "complete"
}

func sgrMousePayload(payload string) bool {
	if len(payload) < 5 || payload[0] != '<' {
		return false
	}
	if payload[len(payload)-1] != 'M' && payload[len(payload)-1] != 'm' {
		return false
	}
	parts := strings.Split(payload[1:len(payload)-1], ";")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}

// stringSequenceStatus reports on an OSC, DCS, or APC string: it completes at
// ST (ESC \), and an OSC also at BEL when bel is set.
func stringSequenceStatus(data string, bel bool) string {
	if strings.HasSuffix(data, escByte+"\\") || bel && strings.HasSuffix(data, "\x07") {
		return "complete"
	}
	return "incomplete"
}

func extractCompleteSequences(buffer string) ([]string, string) {
	var sequences []string
	for pos := 0; pos < len(buffer); {
		remaining := buffer[pos:]
		if strings.HasPrefix(remaining, escByte) {
			foundComplete := false
			seqEnd := 1
			for seqEnd <= len(remaining) {
				candidate := remaining[:seqEnd]
				status := isCompleteSequence(candidate)
				switch status {
				case "complete":
					// When candidate is ESC+ESC and a sequence-introducer
					// follows, emit only the first ESC and restart from the
					// second (WezTerm Kitty-keyboard concatenates the Escape
					// press '\x1b' with a following CSI-u release). When seqEnd
					// is past the end (ESC+ESC at the buffer tail) this falls
					// through; the bounds check keeps the peek from panicking.
					if candidate == escByte+escByte && seqEnd < len(remaining) {
						nextChar := remaining[seqEnd]
						if nextChar == '[' || nextChar == ']' || nextChar == 'O' || nextChar == 'P' || nextChar == '_' {
							sequences = append(sequences, escByte)
							pos++
							foundComplete = true
							seqEnd = len(remaining) + 1
							continue
						}
					}
					sequences = append(sequences, candidate)
					pos += seqEnd
					foundComplete = true
					seqEnd = len(remaining) + 1
				case "incomplete":
					seqEnd++
				default:
					sequences = append(sequences, candidate)
					pos += seqEnd
					foundComplete = true
					seqEnd = len(remaining) + 1
				}
			}
			if foundComplete {
				continue
			}
			return sequences, remaining
		}
		// Emit one character at a time; a byte split would break a
		// multi-byte character apart and defeat Kitty printable dedup.
		_, size := utf8.DecodeRuneInString(remaining)
		sequences = append(sequences, remaining[:size])
		pos += size
	}
	return sequences, ""
}

// dropKeyReleases removes Kitty key-release events (CSI-u with event type :3)
// from chunks bound for the given focused component, honouring a
// tui.KeyReleaseReceiver opt-in. Delegates the per-chunk decision to
// tui.ShouldDeliverKey so modal dispatch shares the one rule that governs the
// editor and modal dialogs.
func dropKeyReleases(component tui.Component, chunks []string) []string {
	return slices.DeleteFunc(chunks, func(chunk string) bool {
		return !tui.ShouldDeliverKey(component, chunk)
	})
}
