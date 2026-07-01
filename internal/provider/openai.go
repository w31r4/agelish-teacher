package provider

import (
	"sort"
	"strings"

	"github.com/zenfun/agelish-teacher/internal/jsonx"
)

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
			if role == "tool" {
				result := openAIChatToolResult(msg)
				parsed.InputMessages = append(parsed.InputMessages, Message{
					Role:  role,
					Parts: []Part{toolResultPart(result.ID, result.Output)},
				})
				parsed.ToolResults = append(parsed.ToolResults, result)
				continue
			}
			parts := openAIContentParts(msg["content"])
			if role == "assistant" {
				for _, call := range openAIChatToolCalls(msg["tool_calls"]) {
					parts = append(parts, toolCallPart(call.ID, call.Name, call.Arguments))
					parsed.ToolCalls = append(parsed.ToolCalls, call)
				}
			}
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
	var chatStream openAIChatCompletionStream
	for _, event := range parseSSE(body) {
		data, ok := eventObject(event)
		if !ok {
			continue
		}
		if isOpenAIChatCompletionChunk(data) {
			chatStream.add(data)
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
	if chatStream.seen {
		return chatStream.parsed(), nil
	}
	return ParsedPayload{}, nil
}

type openAIChatCompletionStream struct {
	seen          bool
	model         string
	content       strings.Builder
	reasoning     strings.Builder
	usage         Usage
	finishReasons []string
	toolCalls     map[int]*openAIChatCompletionToolCall
}

type openAIChatCompletionToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func isOpenAIChatCompletionChunk(data map[string]any) bool {
	if object, ok := data["object"].(string); ok && object == "chat.completion.chunk" {
		return true
	}
	choices, hasChoices := data["choices"].([]any)
	return hasChoices && len(choices) > 0 && data["response"] == nil && data["output"] == nil
}

func (stream *openAIChatCompletionStream) add(data map[string]any) {
	stream.seen = true
	if model := firstString(data["model"]); model != "" {
		stream.model = model
	}
	if usage := usageFrom(data["usage"]); !usage.isZero() {
		stream.usage = usage
	}
	choices, _ := data["choices"].([]any)
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		if finish := firstString(choice["finish_reason"]); finish != "" {
			stream.appendFinishReason(finish)
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		if text := firstString(delta["reasoning_content"], delta["reasoning"]); text != "" {
			stream.reasoning.WriteString(text)
		}
		if text := firstString(delta["content"]); text != "" {
			stream.content.WriteString(text)
		}
		stream.addToolCallDeltas(delta["tool_calls"])
	}
}

func (stream *openAIChatCompletionStream) addToolCallDeltas(raw any) {
	rawCalls, ok := raw.([]any)
	if !ok {
		return
	}
	if stream.toolCalls == nil {
		stream.toolCalls = map[int]*openAIChatCompletionToolCall{}
	}
	for _, rawCall := range rawCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			continue
		}
		index := 0
		if got := asInt64(call["index"]); got != nil {
			index = int(*got)
		}
		accum := stream.toolCalls[index]
		if accum == nil {
			accum = &openAIChatCompletionToolCall{}
			stream.toolCalls[index] = accum
		}
		if id := firstString(call["id"]); id != "" {
			accum.ID = id
		}
		function, _ := call["function"].(map[string]any)
		if name := firstString(function["name"]); name != "" {
			accum.Name = name
		}
		if rawArgs, ok := function["arguments"]; ok {
			switch args := rawArgs.(type) {
			case string:
				accum.Arguments += args
			case nil:
				continue
			default:
				if accum.Arguments == "" {
					accum.Arguments = jsonx.String(args)
				}
			}
		}
	}
}

func (stream *openAIChatCompletionStream) appendFinishReason(reason string) {
	for _, existing := range stream.finishReasons {
		if existing == reason {
			return
		}
	}
	stream.finishReasons = append(stream.finishReasons, reason)
}

func (stream openAIChatCompletionStream) parsed() ParsedPayload {
	parsed := ParsedPayload{
		Model:         stream.model,
		Usage:         stream.usage,
		FinishReasons: stream.finishReasons,
	}
	var parts []Part
	if reasoning := stream.reasoning.String(); reasoning != "" {
		parsed.Reasoning = append(parsed.Reasoning, reasoning)
		parts = append(parts, reasoningPart(reasoning))
	}
	if content := stream.content.String(); content != "" {
		parts = append(parts, textPart(content))
	}
	for _, call := range stream.parsedToolCalls() {
		parsed.ToolCalls = append(parsed.ToolCalls, call)
		parts = append(parts, toolCallPart(call.ID, call.Name, call.Arguments))
	}
	if len(parts) > 0 {
		parsed.OutputMessages = append(parsed.OutputMessages, Message{Role: "assistant", Parts: parts})
	}
	return parsed
}

func (stream openAIChatCompletionStream) parsedToolCalls() []ToolCall {
	if len(stream.toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(stream.toolCalls))
	for index := range stream.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := stream.toolCalls[index]
		if call == nil {
			continue
		}
		calls = append(calls, ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: openAIChatCompletionToolArguments(call.Arguments),
		})
	}
	return calls
}

func openAIChatCompletionToolArguments(raw string) any {
	return openAIToolArguments(raw)
}

func openAIToolArguments(raw any) any {
	trimmed := strings.TrimSpace(firstString(raw))
	if trimmed == "" {
		return map[string]any{}
	}
	var decoded any
	if err := jsonx.Unmarshal([]byte(trimmed), &decoded); err == nil && decoded != nil {
		return decoded
	}
	return firstString(raw)
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

func openAIChatToolCalls(raw any) []ToolCall {
	rawCalls, ok := raw.([]any)
	if !ok {
		return nil
	}
	var calls []ToolCall
	for _, rawCall := range rawCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			continue
		}
		function, _ := call["function"].(map[string]any)
		calls = append(calls, ToolCall{
			ID:        firstString(call["id"], call["call_id"]),
			Name:      firstString(function["name"], call["name"]),
			Arguments: openAIToolArguments(firstNonNil(function["arguments"], call["arguments"])),
		})
	}
	return calls
}

func openAIChatToolResult(msg map[string]any) ToolResult {
	return ToolResult{
		ID:     firstString(msg["tool_call_id"], msg["id"], msg["call_id"]),
		Output: firstNonNil(msg["content"], msg["output"], msg["result"]),
	}
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
					parts = append(parts, imagePart(jsonx.String(part)))
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
			text := jsonx.String(got)
			if text != "" && text != "null" {
				return text
			}
		}
	}
	return ""
}
