package provider

import "strings"

var openAIToolCallTypes = map[string]bool{
	"function_call":         true,
	"custom_tool_call":      true,
	"web_search_call":       true,
	"tool_search_call":      true,
	"image_generation_call": true,
	"local_shell_call":      true,
}

var openAIToolOutputTypes = map[string]bool{
	"function_call_output":    true,
	"custom_tool_call_output": true,
	"tool_search_output":      true,
	"mcp_tool_call_output":    true,
}

func parseOpenAIRequest(body []byte) (ParsedPayload, error) {
	data, ok := loadObject(body)
	if !ok {
		return ParsedPayload{}, nil
	}
	var parsed ParsedPayload
	if instructions, ok := data["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		parsed.SystemInstructions = append(parsed.SystemInstructions, instructions)
		parsed.InputMessages = append(parsed.InputMessages, Message{Role: "system", Parts: []Part{textPart(instructions)}})
	}
	if model, ok := data["model"].(string); ok {
		parsed.Model = model
	}
	parsed.MaxTokens = firstInt(data["max_output_tokens"], data["max_completion_tokens"], data["max_tokens"])

	if messages, ok := data["messages"].([]any); ok {
		for _, rawMsg := range messages {
			msg, ok := rawMsg.(map[string]any)
			if !ok {
				continue
			}
			role := normalizeRole(msg["role"], "user")
			parts := openAIContentParts(msg["content"])
			if len(parts) > 0 {
				parsed.InputMessages = append(parsed.InputMessages, Message{Role: role, Parts: parts})
			}
		}
	}

	switch input := data["input"].(type) {
	case string:
		if input != "" {
			parsed.InputMessages = append(parsed.InputMessages, Message{Role: "user", Parts: []Part{textPart(input)}})
		}
	case []any:
		for _, item := range input {
			messages, calls, results := openAIInputItem(item)
			parsed.InputMessages = append(parsed.InputMessages, messages...)
			parsed.ToolCalls = append(parsed.ToolCalls, calls...)
			parsed.ToolResults = append(parsed.ToolResults, results...)
		}
	}
	return parsed, nil
}

func parseOpenAIResponse(body []byte) (ParsedPayload, error) {
	if data, ok := loadObject(body); ok {
		if response, ok := data["response"].(map[string]any); ok {
			return parseOpenAIResponseObject(response), nil
		}
		return parseOpenAIResponseObject(data), nil
	}

	var completed map[string]any
	var streamOutput []any
	streamOutputByID := map[string]int{}
	argumentsByItemID := map[string]any{}
	for _, event := range parseSSE(body) {
		data, ok := eventObject(event)
		if !ok {
			continue
		}
		typ := eventType(event, data)
		switch typ {
		case "response.completed", "response.done":
			if response, ok := data["response"].(map[string]any); ok {
				completed = response
			} else {
				completed = data
			}
		case "response.function_call_arguments.done":
			itemID := firstString(data["item_id"])
			if itemID != "" {
				argumentsByItemID[itemID] = data["arguments"]
				if pos, ok := streamOutputByID[itemID]; ok {
					if item, ok := streamOutput[pos].(map[string]any); ok {
						item["arguments"] = data["arguments"]
					}
				}
			}
		case "response.output_item.done":
			item, ok := data["item"].(map[string]any)
			if !ok {
				continue
			}
			itemID := firstString(item["id"], item["call_id"])
			if itemID != "" {
				if args, ok := argumentsByItemID[itemID]; ok && item["arguments"] == nil {
					item["arguments"] = args
				}
			}
			appendOpenAIStreamOutputItem(&streamOutput, streamOutputByID, item)
		}
	}
	if completed != nil {
		if len(streamOutput) > 0 {
			if output, ok := completed["output"].([]any); !ok || len(output) == 0 {
				completed["output"] = streamOutput
			}
		}
		return parseOpenAIResponseObject(completed), nil
	}
	if len(streamOutput) > 0 {
		return parseOpenAIResponseObject(map[string]any{"output": streamOutput}), nil
	}
	return ParsedPayload{}, nil
}

func appendOpenAIStreamOutputItem(output *[]any, byID map[string]int, item map[string]any) {
	itemID := firstString(item["id"], item["call_id"])
	if itemID != "" {
		if index, ok := byID[itemID]; ok {
			(*output)[index] = item
			return
		}
		byID[itemID] = len(*output)
	}
	*output = append(*output, item)
}

func parseOpenAIResponseObject(data map[string]any) ParsedPayload {
	var parsed ParsedPayload
	if model, ok := data["model"].(string); ok {
		parsed.Model = model
	}
	if status, ok := data["status"].(string); ok && status != "" {
		parsed.FinishReasons = append(parsed.FinishReasons, status)
	}
	parsed.Usage = usageFrom(data["usage"])

	var assistantParts []Part
	if errorText := openAIErrorText(data); errorText != "" {
		assistantParts = append(assistantParts, textPart(errorText))
		if len(parsed.FinishReasons) == 0 {
			parsed.FinishReasons = append(parsed.FinishReasons, "error")
		}
	}
	if output, ok := data["output"].([]any); ok {
		for _, rawItem := range output {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := item["type"].(string)
			switch {
			case itemType == "message":
				for _, part := range openAIContentParts(item["content"]) {
					assistantParts = append(assistantParts, part)
					if part.Type == "reasoning" {
						if text, ok := part.Content.(string); ok && text != "" {
							parsed.Reasoning = append(parsed.Reasoning, text)
						}
					}
				}
			case itemType == "reasoning":
				for _, part := range openAIReasoningParts(item) {
					assistantParts = append(assistantParts, part)
					if text, ok := part.Content.(string); ok && text != "" {
						parsed.Reasoning = append(parsed.Reasoning, text)
					}
				}
			case openAIToolCallTypes[itemType]:
				call := openAIToolCall(item)
				assistantParts = append(assistantParts, toolCallPart(call.ID, call.Name, call.Arguments))
				parsed.ToolCalls = append(parsed.ToolCalls, call)
			case openAIToolOutputTypes[itemType]:
				result := openAIToolResult(item)
				assistantParts = append(assistantParts, toolResultPart(result.ID, result.Output))
				parsed.ToolResults = append(parsed.ToolResults, result)
			}
		}
	}

	if len(assistantParts) == 0 {
		if choices, ok := data["choices"].([]any); ok {
			for _, rawChoice := range choices {
				choice, ok := rawChoice.(map[string]any)
				if !ok {
					continue
				}
				if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
					parsed.FinishReasons = append(parsed.FinishReasons, finish)
				}
				if msg, ok := choice["message"].(map[string]any); ok {
					assistantParts = append(assistantParts, openAIContentParts(msg["content"])...)
				}
			}
		}
	}

	if len(assistantParts) > 0 {
		parsed.OutputMessages = append(parsed.OutputMessages, Message{Role: "assistant", Parts: assistantParts})
	}
	return parsed
}

func openAIInputItem(raw any) ([]Message, []ToolCall, []ToolResult) {
	item, ok := raw.(map[string]any)
	if !ok {
		if text, ok := raw.(string); ok && text != "" {
			return []Message{{Role: "user", Parts: []Part{textPart(text)}}}, nil, nil
		}
		return nil, nil, nil
	}
	itemType, _ := item["type"].(string)
	switch {
	case itemType == "message":
		role := normalizeRole(item["role"], "user")
		parts := openAIContentParts(item["content"])
		if len(parts) == 0 {
			return nil, nil, nil
		}
		return []Message{{Role: role, Parts: parts}}, nil, toolResultsFromParts(parts)
	case itemType == "input_text":
		if text, ok := item["text"].(string); ok && text != "" {
			return []Message{{Role: "user", Parts: []Part{textPart(text)}}}, nil, nil
		}
	case itemType == "output_text":
		if text, ok := item["text"].(string); ok && text != "" {
			return []Message{{Role: "assistant", Parts: []Part{textPart(text)}}}, nil, nil
		}
	case itemType == "reasoning":
		parts := openAIReasoningParts(item)
		if len(parts) > 0 {
			return []Message{{Role: "assistant", Parts: parts}}, nil, nil
		}
	case openAIToolCallTypes[itemType]:
		call := openAIToolCall(item)
		return []Message{{Role: "assistant", Parts: []Part{toolCallPart(call.ID, call.Name, call.Arguments)}}}, []ToolCall{call}, nil
	case openAIToolOutputTypes[itemType]:
		result := openAIToolResult(item)
		return []Message{{Role: "tool", Parts: []Part{toolResultPart(result.ID, result.Output)}}}, nil, []ToolResult{result}
	}
	return nil, nil, nil
}

func openAIContentParts(raw any) []Part {
	switch content := raw.(type) {
	case string:
		if content != "" {
			return []Part{textPart(content)}
		}
	case map[string]any:
		return openAIContentParts([]any{content})
	case []any:
		var parts []Part
		for _, rawPart := range content {
			switch part := rawPart.(type) {
			case string:
				if part != "" {
					parts = append(parts, textPart(part))
				}
			case map[string]any:
				partType, _ := part["type"].(string)
				switch partType {
				case "input_text", "output_text", "text":
					if text, ok := part["text"].(string); ok && text != "" {
						parts = append(parts, textPart(text))
					}
				case "reasoning_summary_text", "reasoning_content":
					if text, ok := part["text"].(string); ok && text != "" {
						parts = append(parts, reasoningPart(text))
					}
				case "input_image":
					parts = append(parts, imagePart(compactJSON(part)))
				case "encrypted_content":
					if encrypted, ok := part["encrypted_content"].(string); ok && encrypted != "" {
						parts = append(parts, Part{Type: "encrypted_content", Content: map[string]any{"chars": len(encrypted)}})
					}
				default:
					if text, ok := part["text"].(string); ok && text != "" {
						parts = append(parts, textPart(text))
					}
				}
			}
		}
		return parts
	}
	return nil
}

func openAIReasoningParts(item map[string]any) []Part {
	var parts []Part
	if summaries, ok := item["summary"].([]any); ok {
		for _, rawSummary := range summaries {
			summary, ok := rawSummary.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := summary["text"].(string); ok && text != "" {
				parts = append(parts, reasoningPart(text))
			}
		}
	}
	if content, ok := item["content"].([]any); ok {
		for _, part := range openAIContentParts(content) {
			if part.Type == "reasoning" || part.Type == "text" {
				parts = append(parts, reasoningPart(firstString(part.Content)))
			}
		}
	}
	return parts
}

func openAIToolCall(item map[string]any) ToolCall {
	id := firstString(item["call_id"], item["id"])
	name := firstString(item["name"], item["tool_name"])
	namespace := firstString(item["namespace"])
	args := item["arguments"]
	if args == nil {
		args = item["input"]
	}
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{ID: id, Name: name, Namespace: namespace, Arguments: args}
}

func openAIToolResult(item map[string]any) ToolResult {
	id := firstString(item["call_id"], item["id"], item["tool_call_id"])
	output := firstNonNil(item["output"], item["result"], item["content"])
	if output == nil {
		output = ""
	}
	return ToolResult{ID: id, Output: output}
}

func openAIErrorText(data map[string]any) string {
	if text := firstString(data["detail"]); text != "" {
		return text
	}
	if errObj, ok := data["error"].(map[string]any); ok {
		return firstString(errObj["message"], errObj["detail"], errObj["code"])
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstString(values ...any) string {
	for _, value := range values {
		switch got := value.(type) {
		case string:
			if got != "" {
				return got
			}
		case nil:
			continue
		default:
			text := compactJSON(got)
			if text != "" && text != "null" {
				return text
			}
		}
	}
	return ""
}
