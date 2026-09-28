package ai

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func unmarshalJSONWithRepair(data string, target any) error {
	err := json.Unmarshal([]byte(data), target)
	if err == nil {
		return nil
	}
	repaired := repairJSON(data)
	if repaired == data {
		return err
	}
	return json.Unmarshal([]byte(repaired), target)
}

// repairJSON escapes, inside JSON string literals, raw control characters and
// backslashes that do not start a valid JSON escape. Provider payloads and
// streamed tool arguments sometimes carry both.
func repairJSON(data string) string {
	var repaired strings.Builder
	repaired.Grow(len(data))
	inString := false

	for index := 0; index < len(data); index++ {
		char := data[index]
		if !inString {
			repaired.WriteByte(char)
			if char == '"' {
				inString = true
			}
			continue
		}

		switch char {
		case '"':
			repaired.WriteByte(char)
			inString = false
		case '\\':
			if index+1 >= len(data) {
				repaired.WriteString(`\\`)
				continue
			}
			next := data[index+1]
			if next == 'u' && index+5 < len(data) {
				if _, err := strconv.ParseUint(data[index+2:index+6], 16, 16); err == nil {
					repaired.WriteString(data[index : index+6])
					index += 5
					continue
				}
			}
			if strings.ContainsRune(`"\\/bfnrt`, rune(next)) {
				repaired.WriteByte(char)
				repaired.WriteByte(next)
				index++
				continue
			}
			repaired.WriteString(`\\`)
		default:
			switch char {
			case '\b':
				repaired.WriteString(`\b`)
			case '\f':
				repaired.WriteString(`\f`)
			case '\n':
				repaired.WriteString(`\n`)
			case '\r':
				repaired.WriteString(`\r`)
			case '\t':
				repaired.WriteString(`\t`)
			default:
				if char < 0x20 {
					_, _ = fmt.Fprintf(&repaired, `\u%04x`, char)
				} else {
					repaired.WriteByte(char)
				}
			}
		}
	}
	return repaired.String()
}
