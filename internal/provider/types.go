package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type ParsedPayload struct {
	SystemInstructions []string
	InputMessages      []Message
	OutputMessages     []Message
	ToolCalls          []ToolCall
	ToolResults        []ToolResult
	Usage              Usage
	Model              string
	FinishReasons      []string
	MaxTokens          *int64
	Reasoning          []string
	InternalContexts   []InternalContext
}

type InternalContext struct {
	Source    string
	Objective string
	Content   string
}

type Usage struct {
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64
	ReasoningTokens     *int64
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content,omitempty"`
	Parts   []Part `json:"parts"`
}

type Part struct {
	Type       string `json:"type"`
	Content    any    `json:"content,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type ToolCall struct {
	ID        string
	Name      string
	Namespace string
	Arguments any
}

type ToolResult struct {
	ID     string
	Output any
}

func ParseRequest(provider string, body []byte) (ParsedPayload, error) {
	var parsed ParsedPayload
	var err error
	switch normalizeProvider(provider) {
	case "anthropic":
		parsed, err = parseAnthropicRequest(body)
	case "codex", "openai", "openrouter":
		parsed, err = parseOpenAIRequest(body)
	default:
		if data, ok := loadObject(body); ok {
			if _, hasMessages := data["messages"]; hasMessages {
				parsed, err = parseAnthropicRequest(body)
				return finalizeParsed(parsed), err
			}
			if _, hasInput := data["input"]; hasInput {
				parsed, err = parseOpenAIRequest(body)
				return finalizeParsed(parsed), err
			}
		}
		return ParsedPayload{}, nil
	}
	return finalizeParsed(parsed), err
}

func ParseResponse(provider string, body []byte) (ParsedPayload, error) {
	var parsed ParsedPayload
	var err error
	switch normalizeProvider(provider) {
	case "anthropic":
		parsed, err = parseAnthropicResponse(body)
	case "codex", "openai", "openrouter":
		parsed, err = parseOpenAIResponse(body)
	default:
		if data, ok := loadObject(body); ok {
			if _, hasContent := data["content"]; hasContent {
				parsed, err = parseAnthropicResponse(body)
				return finalizeParsed(parsed), err
			}
			if _, hasOutput := data["output"]; hasOutput {
				parsed, err = parseOpenAIResponse(body)
				return finalizeParsed(parsed), err
			}
		}
		return ParsedPayload{}, nil
	}
	return finalizeParsed(parsed), err
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func loadObject(body []byte) (map[string]any, bool) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, false
	}
	return data, true
}

func normalizeRole(raw any, fallback string) string {
	role, _ := raw.(string)
	switch role {
	case "user", "assistant", "system", "tool", "developer":
		return role
	default:
		return fallback
	}
}

func asInt64(raw any) *int64 {
	switch v := raw.(type) {
	case float64:
		i := int64(v)
		return &i
	case int:
		i := int64(v)
		return &i
	case int64:
		i := v
		return &i
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return &i
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return &i
		}
	}
	return nil
}

func usageFrom(raw any) Usage {
	usageObj, ok := raw.(map[string]any)
	if !ok {
		return Usage{}
	}
	usage := Usage{
		InputTokens:         firstInt(usageObj["input_tokens"], usageObj["prompt_tokens"]),
		OutputTokens:        firstInt(usageObj["output_tokens"], usageObj["completion_tokens"]),
		CacheReadTokens:     firstInt(usageObj["cache_read_tokens"], usageObj["cache_read_input_tokens"]),
		CacheCreationTokens: firstInt(usageObj["cache_creation_tokens"], usageObj["cache_creation_input_tokens"]),
		ReasoningTokens:     asInt64(usageObj["reasoning_tokens"]),
	}
	if usage.CacheReadTokens == nil {
		if details, ok := usageObj["input_tokens_details"].(map[string]any); ok {
			usage.CacheReadTokens = asInt64(details["cached_tokens"])
		}
	}
	if usage.ReasoningTokens == nil {
		if details, ok := usageObj["output_tokens_details"].(map[string]any); ok {
			usage.ReasoningTokens = asInt64(details["reasoning_tokens"])
		}
	}
	if usage.ReasoningTokens == nil {
		if details, ok := usageObj["completion_tokens_details"].(map[string]any); ok {
			usage.ReasoningTokens = asInt64(details["reasoning_tokens"])
		}
	}
	return usage
}

func (usage Usage) isZero() bool {
	return usage.InputTokens == nil &&
		usage.OutputTokens == nil &&
		usage.CacheReadTokens == nil &&
		usage.CacheCreationTokens == nil &&
		usage.ReasoningTokens == nil
}

func firstInt(values ...any) *int64 {
	for _, value := range values {
		if got := asInt64(value); got != nil {
			return got
		}
	}
	return nil
}

func compactJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func textPart(text string) Part {
	return Part{Type: "text", Content: text}
}

func reasoningPart(text string) Part {
	return Part{Type: "reasoning", Content: text}
}

func toolCallPart(id string, name string, args any) Part {
	return Part{Type: "tool_call", ToolCallID: id, Name: name, Content: args}
}

func toolResultPart(id string, content any) Part {
	return Part{Type: "tool_result", ToolCallID: id, Content: content}
}

func imagePart(summary string) Part {
	return Part{Type: "image", Content: summary}
}

func finalizeParsed(parsed ParsedPayload) ParsedPayload {
	for i := range parsed.InputMessages {
		if parsed.InputMessages[i].Content == nil {
			parsed.InputMessages[i].Content = contentFromParts(parsed.InputMessages[i].Parts)
		}
	}
	for i := range parsed.OutputMessages {
		if parsed.OutputMessages[i].Content == nil {
			parsed.OutputMessages[i].Content = contentFromParts(parsed.OutputMessages[i].Parts)
		}
	}
	parsed.InternalContexts = append(parsed.InternalContexts, extractInternalContexts(parsed.InputMessages)...)
	return parsed
}

var (
	codexInternalContextPattern = regexp.MustCompile(`(?s)<codex_internal_context\s+source=["']([^"']+)["']>(.*?)</codex_internal_context>`)
	codexObjectivePattern       = regexp.MustCompile(`(?s)<objective>\s*(.*?)\s*</objective>`)
)

func extractInternalContexts(messages []Message) []InternalContext {
	var contexts []InternalContext
	for _, message := range messages {
		for _, text := range messageTextSegments(message) {
			for _, match := range codexInternalContextPattern.FindAllStringSubmatch(text, -1) {
				content := strings.TrimSpace(match[2])
				context := InternalContext{
					Source:  strings.TrimSpace(match[1]),
					Content: content,
				}
				if objective := codexObjectivePattern.FindStringSubmatch(content); len(objective) == 2 {
					context.Objective = strings.TrimSpace(objective[1])
				}
				contexts = append(contexts, context)
			}
		}
	}
	return contexts
}

func messageTextSegments(message Message) []string {
	if len(message.Parts) > 0 {
		var texts []string
		for _, part := range message.Parts {
			if part.Type != "text" {
				continue
			}
			if text, ok := part.Content.(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		}
		return texts
	}
	if text, ok := message.Content.(string); ok && strings.TrimSpace(text) != "" {
		return []string{text}
	}
	return nil
}

func contentFromParts(parts []Part) any {
	var values []string
	for _, part := range parts {
		switch part.Type {
		case "text":
			if text, ok := part.Content.(string); ok && strings.TrimSpace(text) != "" {
				values = append(values, text)
			}
		case "tool_result", "tool_call_response":
			if text, ok := part.Content.(string); ok && strings.TrimSpace(text) != "" {
				values = append(values, text)
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	return strings.Join(values, "\n")
}

func toolResultsFromParts(parts []Part) []ToolResult {
	var results []ToolResult
	for _, part := range parts {
		if part.Type != "tool_result" {
			continue
		}
		results = append(results, ToolResult{
			ID:     part.ToolCallID,
			Output: part.Content,
		})
	}
	return results
}
