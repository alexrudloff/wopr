package ai

import "slices"

// ImageOmittedText stands in for an image a model doesn't accept, in user
// messages and tool results alike, so the model still knows one was there.
const ImageOmittedText = "image content omitted because this model does not accept images"

// AcceptsImages reports whether a model with the given input types takes
// images. A nil input means unknown: the caller falls back to its own
// default.
func AcceptsImages(input []string) (accepts, known bool) {
	if input == nil {
		return false, false
	}
	return slices.Contains(input, "image"), true
}

// WithoutImages returns the transcript with every image in user messages and
// tool results replaced by ImageOmittedText, for a model that takes only
// text.
func (context TranscriptContext) WithoutImages() TranscriptContext {
	if context.err != nil {
		return context
	}
	messages := cloneMessages(context.messages)
	changed := false
	for i, message := range messages {
		switch message := message.(type) {
		case UserMessage:
			blocks, ok := message.Content.(UserContentBlocks)
			if !ok {
				continue
			}
			for j, block := range blocks {
				if _, isImage := block.(ImageContent); isImage {
					blocks[j], changed = TextContent{Text: ImageOmittedText}, true
				}
			}
			message.Content = blocks
			messages[i] = message
		case ToolResultMessage:
			for j, block := range message.Content {
				if _, isImage := block.(ImageContent); isImage {
					message.Content[j], changed = TextContent{Text: ImageOmittedText}, true
				}
			}
			messages[i] = message
		}
	}
	if !changed {
		return context
	}
	return TranscriptContext{messages: messages}
}
