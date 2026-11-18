package snapcompact

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func ompUTF16Len(text string) int {
	n := utf8.RuneCountInString(text)
	for _, r := range text {
		if r > 0xffff {
			n++
		}
	}
	return n
}

func ompTruncate(text string, limit int, ratio float64) string {
	if limit < 0 {
		return text
	}
	length := ompUTF16Len(text)
	if length <= limit {
		return text
	}
	units := utf16.Encode([]rune(text))
	head := int(math.Round(float64(limit) * min(1, max(0, ratio))))
	tail := limit - head
	return fmt.Sprintf("%s […%dch elided…] %s", string(utf16.Decode(units[:head])), length-limit, string(utf16.Decode(units[length-tail:])))
}

const ompElidedPattern = `\[(?:…|\.{3})[0-9]+ch elided(?:…|\.{3})\]`
const ompMediaToken = "[\\w!#$%&'*+.^|~-]+"

var ompDataURL = regexp.MustCompile(`(?i)data:([A-Za-z][\w.+-]*/[\w.+-]+(?:;` + ompMediaToken + "=" + ompMediaToken + `)*);base64,([A-Za-z0-9+/=]*(?:\s*` + ompElidedPattern + `\s*[A-Za-z0-9+/=]*)?)(\s*\))?`)
var ompElided = regexp.MustCompile(`\s*` + ompElidedPattern + `\s*`)
var ompDigits = regexp.MustCompile(`[0-9]+`)

func ompMarkdownOpener(text string, index, cursor int) int {
	i := index
	for i > cursor && ompWhitespace(rune(text[i-1])) {
		i--
	}
	if i-2 < cursor || text[i-2:i] != "](" {
		return index
	}
	start := index
	for j := i - 3; j >= cursor; j-- {
		if text[j] == ']' || text[j] == '\n' {
			break
		}
		if text[j] == '[' {
			start = j
		}
	}
	if start > cursor && start < index && text[start-1] == '!' {
		start--
	}
	return start
}

func ompElideData(text string, persisted bool) string {
	if !strings.Contains(strings.ToLower(text), ";base64,") {
		return text
	}
	var b strings.Builder
	cursor := 0
	for _, m := range ompDataURL.FindAllStringSubmatchIndex(text, -1) {
		payload := text[m[4]:m[5]]
		marker := ompElided.FindString(payload)
		_, decodeErr := base64.StdEncoding.DecodeString(payload)
		if !persisted && marker == "" && decodeErr != nil && len(payload) < 40 {
			b.WriteString(text[cursor:m[1]])
			cursor = m[1]
			continue
		}
		n := len(payload)
		if marker != "" {
			removed, _ := strconv.Atoi(ompDigits.FindString(marker))
			n = n - len(marker) + removed
		}
		start := ompMarkdownOpener(text, m[0], cursor)
		b.WriteString(text[cursor:start])
		wrapped := start != m[0] && m[6] >= 0
		if !wrapped {
			b.WriteString(text[start:m[0]])
		}
		fmt.Fprintf(&b, "[data URL omitted: %s, %d base64 chars]", text[m[2]:m[3]], n)
		if !wrapped && m[6] >= 0 {
			b.WriteString(text[m[6]:m[7]])
		}
		cursor = m[1]
	}
	b.WriteString(text[cursor:])
	return b.String()
}

type ompArgument struct {
	key string
	raw json.RawMessage
}

func ompArguments(raw json.RawMessage) []ompArgument {
	var items []ompArgument
	dec := json.NewDecoder(bytes.NewReader(raw))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil
		}
		items = append(items, ompArgument{key, value})
	}
	slices.SortStableFunc(items, func(a, b ompArgument) int {
		return cmp.Compare(ompJSKey(a.key), ompJSKey(b.key))
	})
	return items
}

func ompJSKey(key string) uint32 {
	n, err := strconv.ParseUint(key, 10, 32)
	if err != nil || strconv.FormatUint(n, 10) != key {
		return math.MaxUint32
	}
	return uint32(n)
}

func ompJSONString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func ompJSONValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	switch raw[0] {
	case '{':
		items := ompArguments(raw)
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, ompJSONString(item.key)+":"+ompJSONValue(item.raw))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case '[':
		var values []json.RawMessage
		_ = json.Unmarshal(raw, &values)
		parts := make([]string, 0, len(values))
		for _, value := range values {
			parts = append(parts, ompJSONValue(value))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return ompJSONString(s)
	case 't', 'f', 'n':
		return string(raw)
	default:
		n, _ := strconv.ParseFloat(string(raw), 64)
		if n == 0 {
			return "0"
		}
		encoded, err := json.Marshal(n)
		if err != nil {
			return "null"
		}
		return string(encoded)
	}
}

var ompRemoveDim = strings.NewReplacer(string(ompDimOn), "", string(ompDimOff), "")

// OMPSerializeConfig controls the upstream tool limits. A negative limit
// keeps the entire value. The zero limit elides the entire value.
type OMPSerializeConfig struct {
	ToolResultChars, ToolArgumentChars, ToolCallChars int
	HeadRatio                                         float64
	DimToolResults                                    bool
}

func DefaultOMPSerializeConfig() OMPSerializeConfig {
	return OMPSerializeConfig{2000, 500, 2000, 0.6, true}
}

// SerializeOMP emits the reference archive transcript before normalization.
func SerializeOMP(messages []OMPMessage, includeThinking bool) string {
	return SerializeOMPWithConfig(messages, includeThinking, DefaultOMPSerializeConfig())
}

func SerializeOMPWithConfig(messages []OMPMessage, includeThinking bool, config OMPSerializeConfig) string {
	if math.IsNaN(config.HeadRatio) {
		config.HeadRatio = DefaultOMPSerializeConfig().HeadRatio
	}
	results := make(map[string]string)
	useless := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role != "toolResult" {
			continue
		}
		if msg.Useless && !msg.IsError {
			useless[msg.ToolCallID] = true
			continue
		}
		if text := ompText(msg.Content); text != "" {
			results[msg.ToolCallID] = text
		}
	}
	merged := make(map[string]bool)
	var out strings.Builder
	var role string
	endsLine := false
	push := func(kind, text string) {
		if kind != role {
			if role != "" {
				out.WriteString("\n\n")
			}
			fmt.Fprintf(&out, "¶%s:", kind)
			role = kind
		} else if !endsLine && !strings.HasPrefix(text, "\n") {
			out.WriteByte('\n')
		}
		out.WriteString(text)
		endsLine = strings.HasSuffix(text, "\n")
	}
	resultBlock := func(text string) string {
		value := ompTruncate(ompElideData(ompRemoveDim.Replace(text), false), config.ToolResultChars, config.HeadRatio)
		if config.DimToolResults {
			value = string(ompDimOn) + value + string(ompDimOff)
		}
		return "<out>\n" + value + "\n</out>"
	}
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if text := ompText(msg.Content); text != "" {
				push("user", ompRemoveDim.Replace(text))
			}
		case "assistant":
			var pendingText, pendingThinking []string
			flush := func() {
				if len(pendingThinking) > 0 {
					push("think", strings.Join(pendingThinking, "\n"))
					pendingThinking = pendingThinking[:0]
				}
				if len(pendingText) > 0 {
					push("ai", strings.Join(pendingText, "\n"))
					pendingText = pendingText[:0]
				}
			}
			for _, block := range msg.Content {
				switch block.Type {
				case "text", "thinking":
					text := ompRemoveDim.Replace(block.Text)
					if strings.TrimFunc(text, ompWhitespace) == "" {
						continue
					}
					if block.Type == "text" {
						pendingText = append(pendingText, text)
					} else if includeThinking {
						pendingThinking = append(pendingThinking, text)
					}
				case "toolCall":
					if useless[block.ID] {
						continue
					}
					flush()
					intent := block.Intent
					var parts []string
					arguments := ompArguments(block.Arguments)
					if len(arguments) == 0 && block.Text != "" && !json.Valid(block.Arguments) {
						parts = append(parts, "input="+ompTruncate(ompElideData(ompJSONString(block.Text), false), config.ToolArgumentChars, config.HeadRatio))
					}
					for _, a := range arguments {
						if a.key == "i" {
							if intent == "" {
								_ = json.Unmarshal(a.raw, &intent)
							}
							continue
						}
						parts = append(parts, a.key+"="+ompTruncate(ompElideData(ompJSONValue(a.raw), false), config.ToolArgumentChars, config.HeadRatio))
					}
					line := block.Name + "(" + ompTruncate(strings.Join(parts, ", "), config.ToolCallChars, config.HeadRatio) + ")"
					if intent = strings.Join(strings.FieldsFunc(ompRemoveDim.Replace(intent), ompWhitespace), " "); intent != "" {
						line += "//" + intent
					}
					if value, ok := results[block.ID]; ok {
						line += "\n" + resultBlock(value)
						merged[block.ID] = true
					}
					push("call", line)
				}
			}
			flush()
		case "toolResult":
			if value, ok := results[msg.ToolCallID]; ok && !useless[msg.ToolCallID] && !merged[msg.ToolCallID] {
				push("call", "\n"+resultBlock(value))
			}
		}
	}
	return out.String()
}

func ompText(content []OMPContent) string {
	var b strings.Builder
	for _, block := range content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
