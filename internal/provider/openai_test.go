package provider

import (
	"strings"
	"testing"
)

func TestOpenAIResponsesCompletedEventMapsHeterogeneousOutput(t *testing.T) {
	raw := []byte("event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5-codex\",\"status\":\"completed\",\"output\":[" +
		"{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"I checked the trace.\"}]}," +
		"{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"date\\\"}\"}," +
		"{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Done.\"}]}" +
		"],\"usage\":{\"input_tokens\":11,\"output_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":5},\"output_tokens_details\":{\"reasoning_tokens\":6}}}}\n\n")

	out, err := ParseResponse("codex", raw)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if out.Model != "gpt-5-codex" {
		t.Fatalf("model mismatch: %q", out.Model)
	}
	if len(out.FinishReasons) != 1 || out.FinishReasons[0] != "completed" {
		t.Fatalf("finish reasons mismatch: %#v", out.FinishReasons)
	}
	if out.Usage.CacheReadTokens == nil || *out.Usage.CacheReadTokens != 5 {
		t.Fatalf("cached token breakdown not mapped: %#v", out.Usage.CacheReadTokens)
	}
	if out.Usage.ReasoningTokens == nil || *out.Usage.ReasoningTokens != 6 {
		t.Fatalf("reasoning token breakdown not mapped: %#v", out.Usage.ReasoningTokens)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "call_1" || out.ToolCalls[0].Name != "shell" {
		t.Fatalf("tool calls mismatch: %#v", out.ToolCalls)
	}
	if len(out.OutputMessages) != 1 {
		t.Fatalf("expected assistant output message, got %#v", out.OutputMessages)
	}
	if out.OutputMessages[0].Content != "Done." {
		t.Fatalf("assistant content mismatch: %#v", out.OutputMessages[0])
	}
	if len(out.Reasoning) != 1 || out.Reasoning[0] != "I checked the trace." {
		t.Fatalf("reasoning summary mismatch: %#v", out.Reasoning)
	}
}

func TestOpenAIResponsesOutputItemDoneMapsStreamingToolCalls(t *testing.T) {
	raw := []byte("event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5-codex\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"name\":\"exec_command\",\"call_id\":\"call_1\"}}\n\n" +
		"event: response.function_call_arguments.done\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"output_index\":0,\"arguments\":\"{\\\"cmd\\\":\\\"rtk date\\\",\\\"yield_time_ms\\\":10000}\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"exec_command\",\"call_id\":\"call_1\",\"arguments\":\"{\\\"cmd\\\":\\\"rtk date\\\",\\\"yield_time_ms\\\":10000}\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5-codex\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n")

	out, err := ParseResponse("codex", raw)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected one streaming tool call, got %#v", out.ToolCalls)
	}
	if out.ToolCalls[0].ID != "call_1" || out.ToolCalls[0].Name != "exec_command" {
		t.Fatalf("tool call identity mismatch: %#v", out.ToolCalls[0])
	}
	if len(out.OutputMessages) != 1 || len(out.OutputMessages[0].Parts) != 1 {
		t.Fatalf("expected assistant tool-call message, got %#v", out.OutputMessages)
	}
	if out.OutputMessages[0].Parts[0].Type != "tool_call" {
		t.Fatalf("expected tool_call output part, got %#v", out.OutputMessages[0].Parts[0])
	}
}

func TestOpenAIChatCompletionsChunkSSEMapsAssistantOutputAndReasoning(t *testing.T) {
	raw := []byte("data: {\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-v4-pro\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null,\"reasoning_content\":\"Plan\"},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-v4-pro\",\"choices\":[{\"delta\":{\"content\":null,\"reasoning_content\":\" done\"},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-v4-pro\",\"choices\":[{\"delta\":{\"content\":\"reason\",\"reasoning_content\":null},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-v4-pro\",\"choices\":[{\"delta\":{\"content\":\"ix ok\",\"reasoning_content\":null},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n" +
		"data: [DONE]\n\n")

	out, err := ParseResponse("openai", raw)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if out.Model != "deepseek-v4-pro" {
		t.Fatalf("model mismatch: %q", out.Model)
	}
	if len(out.OutputMessages) != 1 || out.OutputMessages[0].Content != "reasonix ok" {
		t.Fatalf("assistant output mismatch: %#v", out.OutputMessages)
	}
	if len(out.Reasoning) != 1 || out.Reasoning[0] != "Plan done" {
		t.Fatalf("reasoning mismatch: %#v", out.Reasoning)
	}
	if len(out.FinishReasons) != 1 || out.FinishReasons[0] != "stop" {
		t.Fatalf("finish reason mismatch: %#v", out.FinishReasons)
	}
	if out.Usage.InputTokens == nil || *out.Usage.InputTokens != 3 {
		t.Fatalf("input usage mismatch: %#v", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens == nil || *out.Usage.OutputTokens != 4 {
		t.Fatalf("output usage mismatch: %#v", out.Usage.OutputTokens)
	}
	if out.Usage.ReasoningTokens == nil || *out.Usage.ReasoningTokens != 2 {
		t.Fatalf("reasoning usage mismatch: %#v", out.Usage.ReasoningTokens)
	}
}

func TestOpenAIRequestMapsInstructionsInputAndToolResults(t *testing.T) {
	raw := []byte(`{
		"instructions": "You are Codex.",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run a command."}]},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"max_output_tokens": 2048
	}`)

	in, err := ParseRequest("codex", raw)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(in.SystemInstructions) != 1 || in.SystemInstructions[0] != "You are Codex." {
		t.Fatalf("instructions mismatch: %#v", in.SystemInstructions)
	}
	if len(in.InputMessages) != 3 {
		t.Fatalf("expected system/user/tool messages, got %#v", in.InputMessages)
	}
	if len(in.ToolResults) != 1 || in.ToolResults[0].ID != "call_1" || in.ToolResults[0].Output != "ok" {
		t.Fatalf("tool results mismatch: %#v", in.ToolResults)
	}
	if got := in.MaxTokens; got == nil || *got != 2048 {
		t.Fatalf("max_output_tokens not extracted: %#v", got)
	}
}

func TestOpenAIRequestExtractsCodexGoalContext(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<codex_internal_context source=\"goal\">\nContinue working toward the active thread goal.\n<objective>\nMake Codex traces readable.\n</objective>\n</codex_internal_context>\nNow continue."}]}
		]
	}`)

	in, err := ParseRequest("codex", raw)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(in.InternalContexts) != 1 {
		t.Fatalf("expected one internal context, got %#v", in.InternalContexts)
	}
	got := in.InternalContexts[0]
	if got.Source != "goal" {
		t.Fatalf("goal source mismatch: %#v", got)
	}
	if got.Objective != "Make Codex traces readable." {
		t.Fatalf("goal objective mismatch: %#v", got)
	}
	if !strings.Contains(got.Content, "Continue working toward the active thread goal") {
		t.Fatalf("goal content mismatch: %#v", got)
	}
}

func TestOpenAIErrorDetailMapsOutputMessage(t *testing.T) {
	raw := []byte(`{"detail":"The requested model requires a newer version of Codex."}`)

	out, err := ParseResponse("codex", raw)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(out.OutputMessages) != 1 {
		t.Fatalf("expected error output message, got %#v", out.OutputMessages)
	}
	if out.OutputMessages[0].Content != "The requested model requires a newer version of Codex." {
		t.Fatalf("error output mismatch: %#v", out.OutputMessages[0])
	}
	if len(out.FinishReasons) != 1 || out.FinishReasons[0] != "error" {
		t.Fatalf("finish reason mismatch: %#v", out.FinishReasons)
	}
}
