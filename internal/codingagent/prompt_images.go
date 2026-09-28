package codingagent

import (
	"encoding/base64"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/imageprocessing"
)

// NormalizePromptContent applies the selected model's profile once, before new
// images enter history. Prompt failures become text hints, unlike tool results.
func NormalizePromptContent(content []ai.UserContentBlock, autoResize bool, model *ai.Model) []ai.UserContentBlock {
	var options *ai.ModelImageResizeOptions
	if model != nil && model.InputLimits != nil && model.InputLimits.Images != nil {
		options = model.InputLimits.Images.Resize
	}
	normalized := make([]ai.UserContentBlock, 0, len(content))
	var hints []string
	for _, block := range content {
		image, ok := block.(ai.ImageContent)
		if !ok {
			normalized = append(normalized, block)
			continue
		}
		decoded := imageprocessing.DecodeBase64(image.Data)
		data, mime, hint, err := imageprocessing.ProcessImage(decoded, image.MimeType, autoResize, options)
		if err != nil {
			hints = append(hints, err.Error())
			continue
		}
		normalized = append(normalized, ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime})
		if hint != "" {
			hints = append(hints, hint)
		}
	}
	if len(hints) == 0 {
		return normalized
	}
	hintText := "\n\n" + strings.Join(hints, "\n")
	for i, block := range normalized {
		if text, ok := block.(ai.TextContent); ok {
			text.Text += hintText
			normalized[i] = text
			return normalized
		}
	}
	return append([]ai.UserContentBlock{ai.TextContent{Text: hintText}}, normalized...)
}

func promptContent(text string, images []ai.ImageContent) []ai.UserContentBlock {
	content := make([]ai.UserContentBlock, 0, len(images))
	content = append(content, ai.TextContent{Text: text})
	for _, image := range images {
		content = append(content, image)
	}
	return content
}
