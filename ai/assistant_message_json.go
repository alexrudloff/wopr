package ai

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSON decodes the AssistantMessage wire shape, including
// its closed content-block union.
func (message *AssistantMessage) UnmarshalJSON(data []byte) error {
	type plain AssistantMessage
	var wire struct {
		plain
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	decoded := AssistantMessage(wire.plain)
	if wire.Content != nil {
		decoded.Content = make([]AssistantContentBlock, 0, len(wire.Content))
		for index, raw := range wire.Content {
			block, err := UnmarshalContentBlock(raw)
			if err != nil {
				return fmt.Errorf("assistant content[%d]: %w", index, err)
			}
			assistantBlock, ok := block.(AssistantContentBlock)
			if !ok {
				return fmt.Errorf("assistant content[%d]: %s is not an assistant content block", index, block.contentType())
			}
			decoded.Content = append(decoded.Content, assistantBlock)
		}
	}
	*message = decoded
	return nil
}
