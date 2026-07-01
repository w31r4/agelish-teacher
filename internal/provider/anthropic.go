package provider

import "github.com/zenfun/agelish-teacher/internal/jsonx"

func parseAnthropicRequest(body []byte) (ParsedPayload, error) {
	data, ok := loadObject(body)
	if !ok {
		return ParsedPayload{}, nil
	}
	var parsed ParsedPayload
	if text := anthropicContentText(data["system"]); text != "" {
		parsed.SystemInstructions = append(parsed.SystemInstructions, text)
		parsed.InputMessages = append(parsed.InputMessages, Message{Role: "system", Parts: []Part{textPart(text)}})
	}
	parsed.MaxTokens = asInt64(data["max_tokens"])

	messages, _ := data["messages"].([]any)
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeRole(msg["role"], "user")
		parts, calls, results := anthropicParts(msg["content"])
		for _, call := range calls {
			parsed.ToolCalls = append(parsed.ToolCalls, call)
		}
		parsed.ToolResults = append(parsed.ToolResults, results...)
		if len(parts) > 0 {
			parsed.InputMessages = append(parsed.InputMessages, Message{Role: role, Parts: parts})
		}
	}
	return parsed, nil
}

func parseAnthropicResponse(body []byte) (ParsedPayload, error) {
	if data, ok := loadObject(body); ok {
		return parseAnthropicResponseObject(data), nil
	}
	return parseAnthropicSSE(body), nil
}

func parseAnthropicResponseObject(data map[string]any) ParsedPayload {
	var parsed ParsedPayload
	if model, ok := data["model"].(string); ok {
		parsed.Model = model
	}
	if stop, ok := data["stop_reason"].(string); ok && stop != "" {
		parsed.FinishReasons = append(parsed.FinishReasons, stop)
	}
	parsed.Usage = usageFrom(data["usage"])

	parts, calls, results := anthropicParts(data["content"])
	for _, part := range parts {
		if part.Type == "reasoning" {
			if text, ok := part.Content.(string); ok && text != "" {
				parsed.Reasoning = append(parsed.Reasoning, text)
			}
		}
	}
	parsed.ToolCalls = append(parsed.ToolCalls, calls...)
	parsed.ToolResults = append(parsed.ToolResults, results...)
	if len(parts) > 0 {
		parsed.OutputMessages = append(parsed.OutputMessages, Message{Role: "assistant", Parts: parts})
	}
	return parsed
}

func parseAnthropicSSE(body []byte) ParsedPayload {
	var parsed ParsedPayload
	var currentType string
	var currentID string
	var currentName string
	var textBuf string
	var thinkingBuf string
	var inputBuf string

	finalizeBlock := func() {
		switch currentType {
		case "text":
			if textBuf != "" {
				appendOutputPart(&parsed, textPart(textBuf))
			}
		case "thinking":
			if thinkingBuf != "" {
				parsed.Reasoning = append(parsed.Reasoning, thinkingBuf)
				appendOutputPart(&parsed, reasoningPart(thinkingBuf))
			}
		case "tool_use":
			var args any = map[string]any{}
			if inputBuf != "" {
				if err := jsonx.Unmarshal([]byte(inputBuf), &args); err != nil {
					args = map[string]any{"_raw": inputBuf}
				}
			}
			appendOutputPart(&parsed, toolCallPart(currentID, currentName, args))
			parsed.ToolCalls = append(parsed.ToolCalls, ToolCall{ID: currentID, Name: currentName, Arguments: args})
		}
		currentType, currentID, currentName = "", "", ""
		textBuf, thinkingBuf, inputBuf = "", "", ""
	}

	for _, event := range parseSSE(body) {
		data, ok := eventObject(event)
		if !ok {
			continue
		}
		switch event.Event {
		case "message_start":
			if msg, ok := data["message"].(map[string]any); ok {
				if model, ok := msg["model"].(string); ok {
					parsed.Model = model
				}
				parsed.Usage = mergeUsage(parsed.Usage, usageFrom(msg["usage"]))
			}
		case "content_block_start":
			block, _ := data["content_block"].(map[string]any)
			currentType, _ = block["type"].(string)
			currentID, _ = block["id"].(string)
			currentName, _ = block["name"].(string)
			if currentType == "text" {
				textBuf, _ = block["text"].(string)
			}
			if currentType == "thinking" {
				thinkingBuf, _ = block["thinking"].(string)
			}
		case "content_block_delta":
			delta, _ := data["delta"].(map[string]any)
			switch delta["type"] {
			case "text_delta":
				if text, ok := delta["text"].(string); ok {
					textBuf += text
				}
			case "thinking_delta":
				if text, ok := delta["thinking"].(string); ok {
					thinkingBuf += text
				}
			case "input_json_delta":
				if partial, ok := delta["partial_json"].(string); ok {
					inputBuf += partial
				}
			}
		case "content_block_stop":
			finalizeBlock()
		case "message_delta":
			if delta, ok := data["delta"].(map[string]any); ok {
				if stop, ok := delta["stop_reason"].(string); ok && stop != "" {
					parsed.FinishReasons = append(parsed.FinishReasons, stop)
				}
			}
			parsed.Usage = mergeUsage(parsed.Usage, usageFrom(data["usage"]))
		}
	}
	return parsed
}

func anthropicParts(raw any) ([]Part, []ToolCall, []ToolResult) {
	switch value := raw.(type) {
	case string:
		if value == "" {
			return nil, nil, nil
		}
		return []Part{textPart(value)}, nil, nil
	case []any:
		var parts []Part
		var calls []ToolCall
		var results []ToolResult
		for _, item := range value {
			switch block := item.(type) {
			case string:
				if block != "" {
					parts = append(parts, textPart(block))
				}
			case map[string]any:
				blockType, _ := block["type"].(string)
				switch blockType {
				case "text":
					if text, ok := block["text"].(string); ok && text != "" {
						parts = append(parts, textPart(text))
					}
				case "thinking":
					if text, ok := block["thinking"].(string); ok && text != "" {
						parts = append(parts, reasoningPart(text))
					} else if text, ok := block["text"].(string); ok && text != "" {
						parts = append(parts, reasoningPart(text))
					}
				case "tool_use":
					id, _ := block["id"].(string)
					name, _ := block["name"].(string)
					args := block["input"]
					if args == nil {
						args = map[string]any{}
					}
					parts = append(parts, toolCallPart(id, name, args))
					calls = append(calls, ToolCall{ID: id, Name: name, Arguments: args})
				case "tool_result":
					id, _ := block["tool_use_id"].(string)
					content := block["content"]
					if content == nil {
						content = ""
					}
					parts = append(parts, toolResultPart(id, content))
					results = append(results, ToolResult{ID: id, Output: content})
				case "image":
					parts = append(parts, imagePart(jsonx.String(block)))
				default:
					if text, ok := block["text"].(string); ok && text != "" {
						parts = append(parts, textPart(text))
					}
				}
			}
		}
		return parts, calls, results
	default:
		return nil, nil, nil
	}
}

func anthropicContentText(raw any) string {
	parts, _, _ := anthropicParts(raw)
	var text string
	for _, part := range parts {
		if part.Type == "text" {
			if value, ok := part.Content.(string); ok {
				if text != "" {
					text += "\n"
				}
				text += value
			}
		}
	}
	return text
}

func appendOutputPart(parsed *ParsedPayload, part Part) {
	if len(parsed.OutputMessages) == 0 {
		parsed.OutputMessages = append(parsed.OutputMessages, Message{Role: "assistant"})
	}
	parsed.OutputMessages[0].Parts = append(parsed.OutputMessages[0].Parts, part)
}

func mergeUsage(left Usage, right Usage) Usage {
	if right.InputTokens != nil {
		left.InputTokens = right.InputTokens
	}
	if right.OutputTokens != nil {
		left.OutputTokens = right.OutputTokens
	}
	if right.CacheReadTokens != nil {
		left.CacheReadTokens = right.CacheReadTokens
	}
	if right.CacheCreationTokens != nil {
		left.CacheCreationTokens = right.CacheCreationTokens
	}
	if right.ReasoningTokens != nil {
		left.ReasoningTokens = right.ReasoningTokens
	}
	return left
}
