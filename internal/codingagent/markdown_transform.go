package codingagent

// MarkdownMessageType identifies the transcript message being transformed for
// display. Transformers never change model context or persisted message data.
type MarkdownMessageType string

const (
	MarkdownMessageAssistant MarkdownMessageType = "assistant"
	// MarkdownMessageAssistantThinking marks the reasoning/thinking trace of an
	// assistant turn; the built-in Mermaid transformer skips it.
	MarkdownMessageAssistantThinking MarkdownMessageType = "assistant-thinking"
)

// MarkdownTransformContext describes the message a display-only Markdown
// rewrite is rewriting.
type MarkdownTransformContext struct {
	MessageType    MarkdownMessageType
	IsStreaming    bool
	AvailableWidth int
}
