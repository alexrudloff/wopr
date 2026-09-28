package tui

// loader.go: animated loader component.
//
// A Loader renders a blank line followed by a Text with paddingX=1,
// paddingY=0. The text content is the styled indicator (spinner frame +
// space) concatenated with the styled message.

// DefaultSpinnerFrames is the default Braille spinner.
var DefaultSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// loaderPaddingX is the Loader's horizontal text padding.
const loaderPaddingX = 1

// Loader shows an animated spinner with a message.
type Loader struct {
	invalidatable
	Message      string
	Frame        int
	Frames       []string
	SpinnerColor string // ANSI fg escape for spinner (optional)
	MessageColor string // ANSI fg escape for message (optional)
}

// NewStyledLoader creates a Loader with ANSI color escapes.
func NewStyledLoader(spinnerColor, messageColor, message string, frames []string) *Loader {
	if len(frames) == 0 {
		frames = append([]string{}, DefaultSpinnerFrames...)
	}
	return &Loader{
		Message:      message,
		Frames:       frames,
		SpinnerColor: spinnerColor,
		MessageColor: messageColor,
	}
}

// Render produces ["", paddedLine...]: a blank line, then the content as
// Text with paddingX=1.
func (l *Loader) Render(width int) []string {
	content := l.styledMessage()
	if indicator := l.renderedIndicator(); indicator != "" {
		content = indicator + " " + content
	}

	return append([]string{""}, NewPaddedText(content, loaderPaddingX, 0, nil).Render(width)...)
}

func (l *Loader) styledMessage() string {
	if l.MessageColor != "" {
		return l.MessageColor + l.Message + "\x1b[0m"
	}
	return l.Message
}

// SetMessage replaces the message and marks the loader for repaint.
func (l *Loader) SetMessage(message string) {
	l.Message = message
	l.Invalidate()
}

// Tick advances the spinner one frame.
func (l *Loader) Tick() {
	l.Frame++
	l.Invalidate()
}

func (l *Loader) renderedIndicator() string {
	if len(l.Frames) == 0 {
		return ""
	}
	frame := l.Frames[l.Frame%len(l.Frames)]
	if l.SpinnerColor != "" {
		return l.SpinnerColor + frame + "\x1b[0m"
	}
	return frame
}
