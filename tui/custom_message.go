package tui

// custom_message.go: renders custom session messages as colored text
// lines inside a padded, background-tinted box.

import (
	"fmt"
	"strings"
)

// CustomMessageComponent renders a custom message entry.
type CustomMessageComponent struct {
	invalidatable
	CustomType string
	Content    string // text content (may contain markdown)
	Expanded   bool
}

// NewCustomMessageComponent creates a custom message renderer.
func NewCustomMessageComponent(customType, content string) *CustomMessageComponent {
	return &CustomMessageComponent{
		CustomType: customType,
		Content:    content,
	}
}

// SetExpanded toggles between collapsed and expanded rendering.
func (c *CustomMessageComponent) SetExpanded(expanded bool) {
	c.Expanded = expanded
	c.Invalidate()
}

// SetOutputPad invalidates the default renderer, whose box keeps its fixed inset.
// Only registered custom renderers consume the configured horizontal padding.
func (c *CustomMessageComponent) SetOutputPad(_ int) {
	c.Invalidate()
}

// Render produces the custom message lines.
// The box adds paddingX=1 (space indent) and paddingY=1 (blank rows) with bg tint.
func (c *CustomMessageComponent) Render(width int) []string {
	width = max(width, 4)
	t := ActiveTheme()
	customMsgBgOpen := t.CustomMessageBg
	var lines []string

	// The component spacer is outside the box's background-painted top padding.
	lines = append(lines, "", paintBgWith(customMsgBgOpen, "", width))

	const padding = " "
	// Label: [customType] in bold.
	labelStyled := t.CustomMessageLabel + "\x1b[1m[" + c.CustomType + "]\x1b[22m\x1b[0m"
	lines = append(lines, paintBgWith(customMsgBgOpen, padding+labelStyled, width))

	// Structural blank between label and content.
	lines = append(lines, paintBgWith(customMsgBgOpen, "", width))

	if c.Content != "" {
		// Render content as plain text lines with paddingX=1 indent.
		contentColor := t.CustomMessageText
		for raw := range strings.SplitSeq(c.Content, "\n") {
			var line string
			if contentColor != "" {
				line = padding + contentColor + raw + "\x1b[0m"
			} else {
				line = padding + raw
			}
			lines = append(lines, paintBgWith(customMsgBgOpen, line, width))
		}
	}

	lines = append(lines, paintBgWith(customMsgBgOpen, "", width))
	return lines
}

// CustomMessage holds the data for a custom message entry.
type CustomMessage struct {
	CustomType string
	Content    any // string or []ContentBlock
}

// CustomMessageText extracts text from a CustomMessage's Content field.
func CustomMessageText(cm *CustomMessage) string {
	switch v := cm.Content.(type) {
	case string:
		return v
	case []map[string]any:
		var parts []string
		for _, block := range v {
			if block["type"] == "text" {
				if t, ok := block["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", cm.Content)
	}
}
