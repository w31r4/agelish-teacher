package provider

import (
	"encoding/json"
	"testing"
)

func TestAnthropicMessagesMapToGenAIMessagesAndToolCalls(t *testing.T) {
	request := []byte(`{
		"system": "Be concise.",
		"messages": [
			{"role":"user","content":[
				{"type":"text","text":"Use the calculator."},
				{"type":"tool_result","tool_use_id":"toolu_previous","content":"4"}
			]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_history","name":"calculator","input":{"expression":"1+1"}}
			]}
		],
		"max_tokens": 1024
	}`)
	response := []byte(`{
		"id": "msg_1",
		"model": "claude-3-5-sonnet",
		"stop_reason": "tool_use",
		"content": [
			{"type":"thinking","thinking":"checking arithmetic"},
			{"type":"text","text":"I'll calculate it."},
			{"type":"tool_use","id":"toolu_2","name":"calculator","input":{"expression":"2+2"}}
		],
		"usage": {
			"input_tokens": 10,
			"output_tokens": 20,
			"cache_read_input_tokens": 3,
			"cache_creation_input_tokens": 2
		}
	}`)

	in, err := ParseRequest("anthropic", request)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	out, err := ParseResponse("anthropic", response)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if got := in.SystemInstructions; len(got) != 1 || got[0] != "Be concise." {
		t.Fatalf("system instructions mismatch: %#v", got)
	}
	if len(in.InputMessages) != 3 {
		t.Fatalf("expected system/user/history messages, got %#v", in.InputMessages)
	}
	if len(in.ToolResults) != 1 || in.ToolResults[0].ID != "toolu_previous" || in.ToolResults[0].Output != "4" {
		t.Fatalf("tool results mismatch: %#v", in.ToolResults)
	}
	if got := in.MaxTokens; got == nil || *got != 1024 {
		t.Fatalf("max_tokens not extracted: %#v", got)
	}

	if out.Model != "claude-3-5-sonnet" {
		t.Fatalf("model mismatch: %q", out.Model)
	}
	if len(out.FinishReasons) != 1 || out.FinishReasons[0] != "tool_use" {
		t.Fatalf("finish reasons mismatch: %#v", out.FinishReasons)
	}
	if out.Usage.InputTokens == nil || *out.Usage.InputTokens != 10 {
		t.Fatalf("input tokens mismatch: %#v", out.Usage.InputTokens)
	}
	if out.Usage.CacheReadTokens == nil || *out.Usage.CacheReadTokens != 3 {
		t.Fatalf("cache read tokens mismatch: %#v", out.Usage.CacheReadTokens)
	}
	if out.Usage.CacheCreationTokens == nil || *out.Usage.CacheCreationTokens != 2 {
		t.Fatalf("cache creation tokens mismatch: %#v", out.Usage.CacheCreationTokens)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "toolu_2" || out.ToolCalls[0].Name != "calculator" {
		t.Fatalf("tool calls mismatch: %#v", out.ToolCalls)
	}
	if out.OutputMessages[0].Content != "I'll calculate it." {
		t.Fatalf("assistant content mismatch: %#v", out.OutputMessages[0])
	}

	raw, err := json.Marshal(out.OutputMessages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	if !json.Valid(raw) || len(raw) == 0 {
		t.Fatalf("output messages must be JSON serializable: %q", raw)
	}
}

func TestAnthropicSSEReassemblesTextThinkingToolUseAndUsage(t *testing.T) {
	raw := []byte("event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-3-5-sonnet\",\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"plan\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\" more\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"Hel\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"calculator\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"expression\\\":\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"2+2\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":2}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n")

	out, err := ParseResponse("anthropic", raw)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if out.Model != "claude-3-5-sonnet" {
		t.Fatalf("model mismatch: %q", out.Model)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("tool call not reassembled: %#v", out.ToolCalls)
	}
	if out.Usage.InputTokens == nil || *out.Usage.InputTokens != 7 {
		t.Fatalf("input usage not reassembled: %#v", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens == nil || *out.Usage.OutputTokens != 9 {
		t.Fatalf("output usage not reassembled: %#v", out.Usage.OutputTokens)
	}
}
