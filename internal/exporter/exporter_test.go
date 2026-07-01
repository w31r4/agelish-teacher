package exporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

func TestExporterBuildsSessionTurnGenerationAndToolSpansFromScribeDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "sess_1", "codex", 1710000000000, 1710000005000)
	insertTurn(t, db, "turn_1", "sess_1", 1, "completed", 1710000000100, 1710000004000)
	insertTraceRequest(t, db, traceRow{
		ID: "req_1", SessionID: "sess_1", TurnID: "turn_1", RequestID: "call_1",
		Direction: "request", Provider: "anthropic", Model: "claude-3-5-sonnet",
		RequestedModel: "claude-3-5-sonnet", Timestamp: 1710000000200,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_1", SessionID: "sess_1", TurnID: "turn_1", RequestID: "call_1",
		Direction: "response", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022",
		RequestedModel: "claude-3-5-sonnet", Timestamp: 1710000001200,
		Outcome: "ok", HTTPStatus: 200, StopReason: "tool_use", InputTokens: ptr(10),
		OutputTokens: ptr(20), ToolCallCount: 1, CacheReadTokens: ptr(3),
		CacheCreationTokens: ptr(2), ReasoningTokens: ptr(4), MaxTokens: ptr(1024),
		DurationMS: ptr(1000),
	})
	insertRawPayload(t, db, "raw_req_1", "req_1", "identity", []byte(`{"system":"Be concise.","messages":[{"role":"user","content":"Use calculator."}],"max_tokens":1024}`))
	insertRawPayload(t, db, "raw_resp_1", "resp_1", "identity", []byte(`{"model":"claude-3-5-sonnet-20241022","stop_reason":"tool_use","content":[{"type":"text","text":"I'll calculate it."},{"type":"tool_use","id":"toolu_2","name":"calculator","input":{"expression":"2+2"}}],"usage":{"input_tokens":10,"output_tokens":20}}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if len(result.Spans) != 4 {
		t.Fatalf("expected session, turn, generation, tool spans; got %d: %#v", len(result.Spans), result.Spans)
	}
	generation := findSpanByAttr(t, result.Spans, "langfuse.observation.type", "generation")
	if generation.Kind != "SPAN_KIND_CLIENT" {
		t.Fatalf("generation span kind mismatch: %s", generation.Kind)
	}
	assertAttr(t, generation.Attributes, "gen_ai.provider.name", "anthropic")
	assertAttr(t, generation.Attributes, "gen_ai.request.model", "claude-3-5-sonnet")
	assertAttr(t, generation.Attributes, "gen_ai.response.model", "claude-3-5-sonnet-20241022")
	assertAttr(t, generation.Attributes, "gen_ai.usage.input_tokens", int64(10))
	assertAttr(t, generation.Attributes, "gen_ai.usage.output_tokens", int64(20))
	assertAttr(t, generation.Attributes, "gen_ai.usage.cache_read.input_tokens", int64(3))
	assertAttr(t, generation.Attributes, "gen_ai.usage.cache_creation.input_tokens", int64(2))
	assertAttr(t, generation.Attributes, "gen_ai.usage.reasoning.output_tokens", int64(4))
	assertAttr(t, generation.Attributes, "gen_ai.request.max_tokens", int64(1024))
	assertAttr(t, generation.Attributes, "gen_ai.response.finish_reasons", []string{"tool_use"})
	assertAttr(t, generation.Attributes, "langfuse.session.id", "sess_1")
	if generation.StartUnixNano != 1710000000200*1_000_000 || generation.EndUnixNano != 1710000001200*1_000_000 {
		t.Fatalf("generation timestamps mismatch: start=%d end=%d", generation.StartUnixNano, generation.EndUnixNano)
	}

	var inputMessages []map[string]any
	if err := json.Unmarshal([]byte(generation.Attributes["gen_ai.input.messages"].(string)), &inputMessages); err != nil {
		t.Fatalf("input messages are not JSON: %v", err)
	}
	if len(inputMessages) != 2 {
		t.Fatalf("expected system and user input messages, got %#v", inputMessages)
	}

	tool := findSpanByAttr(t, result.Spans, "gen_ai.operation.name", "execute_tool")
	if tool.ParentSpanID != generation.SpanID {
		t.Fatalf("tool span parent mismatch: got %s want %s", tool.ParentSpanID, generation.SpanID)
	}
	assertAttr(t, tool.Attributes, "gen_ai.tool.name", "calculator")
	assertAttr(t, tool.Attributes, "gen_ai.tool.call.id", "toolu_2")
}

func TestExporterNamesClaudeCodeSessionAndTurnSpans(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "019f1caec3ab7552bf2c7e6a1725ce3d", "claude-code", 1710000000000, 1710000005000)
	insertTurn(t, db, "turn_claude_2", "019f1caec3ab7552bf2c7e6a1725ce3d", 2, "completed", 1710000000100, 1710000004000)

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	session := findSpanByAttr(t, result.Spans, "scribe.session.id", "019f1caec3ab7552bf2c7e6a1725ce3d")
	if session.Name != "Claude Code - Session 019f1caec3ab" {
		t.Fatalf("session name mismatch: %q", session.Name)
	}
	assertAttr(t, session.Attributes, "langfuse.trace.name", "Claude Code - Session 019f1caec3ab")

	turn := findSpanByAttr(t, result.Spans, "scribe.turn.id", "turn_claude_2")
	if turn.Name != "Claude Code - Turn 2" {
		t.Fatalf("turn name mismatch: %q", turn.Name)
	}
	assertAttr(t, turn.Attributes, "langfuse.trace.name", "Claude Code - Session 019f1caec3ab")
}

func TestExporterNamesCodexObservationsForLangfuseTree(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "019f1d000000777788889999aaaabbbb", "codex", 1710000000000, 1710000006000)
	insertTurn(t, db, "turn_codex_3", "019f1d000000777788889999aaaabbbb", 3, "completed", 1710000000100, 1710000005000)
	insertTraceRequest(t, db, traceRow{
		ID: "req_codex_1", SessionID: "019f1d000000777788889999aaaabbbb", TurnID: "turn_codex_3", RequestID: "call_codex_1",
		Direction: "request", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000000200,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_codex_1", SessionID: "019f1d000000777788889999aaaabbbb", TurnID: "turn_codex_3", RequestID: "call_codex_1",
		Direction: "response", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000001200,
		Outcome: "ok", HTTPStatus: 200, StopReason: "requires_action", ToolCallCount: 1,
	})
	insertRawPayload(t, db, "raw_req_codex_1", "req_codex_1", "identity", []byte(`{"input":"Print pwd."}`))
	insertRawPayload(t, db, "raw_resp_codex_1", "resp_codex_1", "identity", []byte(`{"model":"gpt-5-codex","status":"requires_action","output":[{"type":"function_call","call_id":"call_exec_1","name":"exec_command","arguments":{"cmd":"pwd"}}]}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	session := findSpanByAttr(t, result.Spans, "scribe.session.id", "019f1d000000777788889999aaaabbbb")
	if session.Name != "Codex - Session 019f1d000000" {
		t.Fatalf("session name mismatch: %q", session.Name)
	}
	assertAttr(t, session.Attributes, "langfuse.trace.name", "Codex - Session 019f1d000000")

	turn := findSpanByAttr(t, result.Spans, "scribe.turn.id", "turn_codex_3")
	if turn.Name != "Codex - Turn 3" {
		t.Fatalf("turn name mismatch: %q", turn.Name)
	}
	assertAttr(t, turn.Attributes, "langfuse.trace.name", "Codex - Session 019f1d000000")

	generation := findSpanByAttr(t, result.Spans, "langfuse.observation.type", "generation")
	if generation.Name != "Codex gpt-5-codex" {
		t.Fatalf("generation name mismatch: %q", generation.Name)
	}
	assertAttr(t, generation.Attributes, "gen_ai.provider.name", "codex")

	tool := findSpanByAttr(t, result.Spans, "gen_ai.tool.call.id", "call_exec_1")
	if tool.Name != "Shell command" {
		t.Fatalf("tool name mismatch: %q", tool.Name)
	}
	assertAttr(t, tool.Attributes, "gen_ai.tool.name", "exec_command")
}

func TestExporterAddsLangfuseInputOutputAndToolResultOutput(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "sess_tool_results", "codex", 1710000000000, 1710000006000)
	insertTurn(t, db, "turn_tool_results", "sess_tool_results", 1, "completed", 1710000000100, 1710000005000)
	insertTraceRequest(t, db, traceRow{
		ID: "req_1", SessionID: "sess_tool_results", TurnID: "turn_tool_results", RequestID: "call_1",
		Direction: "request", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000000200,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_1", SessionID: "sess_tool_results", TurnID: "turn_tool_results", RequestID: "call_1",
		Direction: "response", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000001200,
		Outcome: "ok", HTTPStatus: 200, StopReason: "tool_use", ToolCallCount: 1,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "req_2", SessionID: "sess_tool_results", TurnID: "turn_tool_results", RequestID: "call_2",
		Direction: "request", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000002200,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_2", SessionID: "sess_tool_results", TurnID: "turn_tool_results", RequestID: "call_2",
		Direction: "response", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000003200,
		Outcome: "ok", HTTPStatus: 200, StopReason: "completed",
	})
	insertRawPayload(t, db, "raw_req_1", "req_1", "identity", []byte(`{"input":"List files before answering."}`))
	insertRawPayload(t, db, "raw_resp_1", "resp_1", "identity", []byte(`{"model":"gpt-5-codex","status":"requires_action","output":[{"type":"function_call","call_id":"call_tool_1","name":"exec_command","arguments":{"cmd":"ls"}}]}`))
	insertRawPayload(t, db, "raw_req_2", "req_2", "identity", []byte(`{"input":[{"type":"function_call_output","call_id":"call_tool_1","output":"README.md\ncmd\ninternal"}]}`))
	insertRawPayload(t, db, "raw_resp_2", "resp_2", "identity", []byte(`{"model":"gpt-5-codex","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Found README.md, cmd, and internal."}]}]}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	session := findSpanByAttr(t, result.Spans, "scribe.session.id", "sess_tool_results")
	assertAttrJSONContains(t, session.Attributes, "langfuse.trace.input", "List files before answering.")
	assertAttrJSONContains(t, session.Attributes, "langfuse.trace.output", "Found README.md, cmd, and internal.")

	turn := findSpanByAttr(t, result.Spans, "scribe.turn.id", "turn_tool_results")
	if turn.Attributes["langfuse.observation.input"] == nil || turn.Attributes["langfuse.observation.output"] == nil {
		t.Fatalf("turn should carry langfuse observation input and output: %#v", turn.Attributes)
	}

	firstGeneration := findSpanByAttrs(t, result.Spans, map[string]any{
		"langfuse.observation.type": "generation",
		"scribe.trace_request.id":   "resp_1",
	})
	if firstGeneration.Attributes["langfuse.observation.input"] == nil || firstGeneration.Attributes["langfuse.observation.output"] == nil {
		t.Fatalf("generation should carry langfuse observation input and output: %#v", firstGeneration.Attributes)
	}

	tool := findSpanByAttr(t, result.Spans, "gen_ai.tool.call.id", "call_tool_1")
	assertAttr(t, tool.Attributes, "langfuse.observation.output", "README.md\ncmd\ninternal")
	assertAttr(t, tool.Attributes, "gen_ai.tool.call.result", "README.md\ncmd\ninternal")

	secondGeneration := findSpanByAttrs(t, result.Spans, map[string]any{
		"langfuse.observation.type": "generation",
		"scribe.trace_request.id":   "resp_2",
	})
	input := secondGeneration.Attributes["langfuse.observation.input"]
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal second generation input: %v", err)
	}
	if !strings.Contains(string(inputJSON), "tool_result") || !strings.Contains(string(inputJSON), "README.md") {
		t.Fatalf("second generation input should include tool result, got %s", inputJSON)
	}
}

func TestExporterMarksMissingToolResultOutput(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "sess_missing_tool", "codex", 1710000000000, 1710000004000)
	insertTurn(t, db, "turn_missing_tool", "sess_missing_tool", 1, "completed", 1710000000100, 1710000003000)
	insertTraceRequest(t, db, traceRow{
		ID: "req_1", SessionID: "sess_missing_tool", TurnID: "turn_missing_tool", RequestID: "call_1",
		Direction: "request", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000000200,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_1", SessionID: "sess_missing_tool", TurnID: "turn_missing_tool", RequestID: "call_1",
		Direction: "response", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000001200,
		Outcome: "ok", HTTPStatus: 200, StopReason: "tool_use", ToolCallCount: 1,
	})
	insertRawPayload(t, db, "raw_req_1", "req_1", "identity", []byte(`{"input":"Run a command."}`))
	insertRawPayload(t, db, "raw_resp_1", "resp_1", "identity", []byte(`{"model":"gpt-5-codex","status":"requires_action","output":[{"type":"function_call","call_id":"call_without_result","name":"exec_command","arguments":{"cmd":"date"}}]}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	tool := findSpanByAttr(t, result.Spans, "gen_ai.tool.call.id", "call_without_result")
	assertAttr(t, tool.Attributes, "scribe.tool.result.status", "missing")
	assertAttrJSONContains(t, tool.Attributes, "langfuse.observation.output", "missing_tool_result")
	if _, ok := tool.Attributes["gen_ai.tool.call.result"]; ok {
		t.Fatalf("missing tool result should not set gen_ai.tool.call.result: %#v", tool.Attributes)
	}
}

func TestExporterMapsCodexModelProbeToControlSpanNotGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "sess_probe", "codex", 1710000000000, 1710000003000)
	insertTurn(t, db, "turn_probe", "sess_probe", 1, "completed", 1710000000100, 1710000002000)
	summary := `{
		"request_role": "probe",
		"fine_role": "control",
		"agent_type": "control",
		"phase": "control",
		"path": "/models?client_version=0.142.4",
		"control_kind": "codex_model_probe",
		"model_count": 5
	}`
	insertTraceRequest(t, db, traceRow{
		ID: "req_probe", SessionID: "sess_probe", TurnID: "turn_probe", RequestID: "probe_1",
		Direction: "request", Provider: "codex", Model: "unknown", Timestamp: 1710000000200,
		Summary: summary,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_probe", SessionID: "sess_probe", TurnID: "turn_probe", RequestID: "probe_1",
		Direction: "response", Provider: "codex", Model: "unknown", Timestamp: 1710000001200,
		Outcome: "ok", HTTPStatus: 200, Summary: summary,
	})
	insertRawPayload(t, db, "raw_req_probe", "req_probe", "identity", []byte{})
	insertRawPayload(t, db, "raw_resp_probe", "resp_probe", "identity", []byte(`{"data":[{"id":"gpt-5-codex"}]}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	control := findSpanByAttrs(t, result.Spans, map[string]any{
		"langfuse.observation.type": "span",
		"scribe.trace_request.id":   "resp_probe",
	})
	if control.Name != "Codex model probe" {
		t.Fatalf("control name mismatch: %q", control.Name)
	}
	if control.Kind != "SPAN_KIND_INTERNAL" {
		t.Fatalf("control span kind mismatch: %s", control.Kind)
	}
	assertAttr(t, control.Attributes, "scribe.control_kind", "codex_model_probe")
	assertAttrJSONContains(t, control.Attributes, "langfuse.observation.input", "/models?client_version=0.142.4")
	assertAttrJSONContains(t, control.Attributes, "langfuse.observation.output", "model_count")
	for _, span := range result.Spans {
		if span.Attributes["scribe.trace_request.id"] == "resp_probe" && span.Attributes["langfuse.observation.type"] == "generation" {
			t.Fatalf("control probe should not produce generation span: %#v", span)
		}
		if span.Attributes["scribe.trace_request.id"] == "resp_probe" && span.Attributes["gen_ai.operation.name"] == "chat" {
			t.Fatalf("control probe should not use GenAI chat operation: %#v", span)
		}
	}
	turn := findSpanByAttr(t, result.Spans, "scribe.turn.id", "turn_probe")
	assertAttrJSONContains(t, turn.Attributes, "langfuse.observation.input", "/models?client_version=0.142.4")
	assertAttrJSONContains(t, turn.Attributes, "langfuse.observation.output", "model_count")
}

func TestExporterDecodesZstdPayloads(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)
	insertSession(t, db, "sess_1", "codex", 1710000000000, 1710000005000)
	insertTurn(t, db, "turn_1", "sess_1", 1, "completed", 1710000000100, 1710000004000)
	insertTraceRequest(t, db, traceRow{ID: "req_1", SessionID: "sess_1", TurnID: "turn_1", RequestID: "call_1", Direction: "request", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000000200})
	insertTraceRequest(t, db, traceRow{ID: "resp_1", SessionID: "sess_1", TurnID: "turn_1", RequestID: "call_1", Direction: "response", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000001200})
	insertRawPayload(t, db, "raw_req_1", "req_1", "identity", []byte(`{"input":"hello"}`))

	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	body := encoder.EncodeAll([]byte(`{"model":"gpt-5-codex","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"world"}]}]}`), nil)
	insertRawPayload(t, db, "raw_resp_1", "resp_1", "zstd", body)

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	generation := findSpanByAttr(t, result.Spans, "langfuse.observation.type", "generation")
	var outputMessages []map[string]any
	if err := json.Unmarshal([]byte(generation.Attributes["gen_ai.output.messages"].(string)), &outputMessages); err != nil {
		t.Fatalf("output messages are not JSON: %v", err)
	}
	if len(outputMessages) != 1 {
		t.Fatalf("expected one output message, got %#v", outputMessages)
	}
}

func TestExporterWrapsSubagentGenerationInAgentObservation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	insertSession(t, db, "sess_subagent", "codex", 1710000000000, 1710000005000)
	insertTurn(t, db, "turn_1", "sess_subagent", 1, "completed", 1710000000100, 1710000004000)
	summary := `{
		"request_role": "subagent",
		"fine_role": "codex_subagent",
		"request_role_classifier": "subagent",
		"request_role_classifier_confidence": "high",
		"codex_turn_id": "codex_turn_1",
		"codex_thread_id": "codex_thread_1"
	}`
	insertTraceRequest(t, db, traceRow{
		ID: "req_subagent", SessionID: "sess_subagent", TurnID: "turn_1", RequestID: "call_subagent",
		Direction: "request", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000000200,
		Summary: summary,
	})
	insertTraceRequest(t, db, traceRow{
		ID: "resp_subagent", SessionID: "sess_subagent", TurnID: "turn_1", RequestID: "call_subagent",
		Direction: "response", Provider: "codex", Model: "gpt-5-codex", Timestamp: 1710000001200,
		Outcome: "ok", HTTPStatus: 200, InputTokens: ptr(10), OutputTokens: ptr(2),
		Summary: summary,
	})
	insertRawPayload(t, db, "raw_req_subagent", "req_subagent", "identity", []byte(`{"input":"inspect this area"}`))
	insertRawPayload(t, db, "raw_resp_subagent", "resp_subagent", "identity", []byte(`{"model":"gpt-5-codex","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	agent := findSpanByAttr(t, result.Spans, "langfuse.observation.type", "agent")
	if agent.Name != "Subagent Codex subagent" {
		t.Fatalf("agent name mismatch: %q", agent.Name)
	}
	assertAttr(t, agent.Attributes, "scribe.request_role", "subagent")
	assertAttr(t, agent.Attributes, "scribe.fine_role", "codex_subagent")
	assertAttr(t, agent.Attributes, "scribe.codex.turn_id", "codex_turn_1")

	generation := findSpanByAttrs(t, result.Spans, map[string]any{
		"langfuse.observation.type": "generation",
		"scribe.trace_request.id":   "resp_subagent",
	})
	if generation.ParentSpanID != agent.SpanID {
		t.Fatalf("generation parent mismatch: got %s want %s", generation.ParentSpanID, agent.SpanID)
	}
	if generation.Name != "Subagent Codex gpt-5-codex" {
		t.Fatalf("generation name mismatch: %q", generation.Name)
	}
	assertAttr(t, generation.Attributes, "scribe.request_role", "subagent")
	assertAttr(t, generation.Attributes, "scribe.fine_role", "codex_subagent")
}

func TestExporterReadsLegacyISOTimestamps(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createScribeSchema(t, db)

	if _, err := db.Exec(`INSERT INTO sessions (id, source, name, started_at, ended_at, metadata) VALUES ('sess_legacy', 'codex', 'legacy', '1970-01-01T00:00:00Z', '1970-01-01T00:00:05Z', '{}')`); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO turns (id, session_id, turn_number, status, started_at, ended_at) VALUES ('turn_legacy', 'sess_legacy', 1, 'completed', '1970-01-01T00:00:01Z', '1970-01-01T00:00:04Z')`); err != nil {
		t.Fatalf("insert legacy turn: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO trace_requests (id, session_id, turn_id, request_id, direction, provider, model, timestamp, summary) VALUES ('req_legacy', 'sess_legacy', 'turn_legacy', 'call_legacy', 'request', 'codex', 'gpt-5-codex', '1970-01-01T00:00:02Z', '{}')`); err != nil {
		t.Fatalf("insert legacy request: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO trace_requests (id, session_id, turn_id, request_id, direction, provider, model, timestamp, summary, duration_ms) VALUES ('resp_legacy', 'sess_legacy', 'turn_legacy', 'call_legacy', 'response', 'codex', 'gpt-5-codex', '1970-01-01T00:00:03Z', '{}', 1000)`); err != nil {
		t.Fatalf("insert legacy response: %v", err)
	}
	insertRawPayload(t, db, "raw_req_legacy", "req_legacy", "identity", []byte(`{"input":"hello"}`))
	insertRawPayload(t, db, "raw_resp_legacy", "resp_legacy", "identity", []byte(`{"model":"gpt-5-codex","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"world"}]}]}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	generation := findSpanByAttr(t, result.Spans, "langfuse.observation.type", "generation")
	if generation.StartUnixNano != 2_000_000_000 || generation.EndUnixNano != 3_000_000_000 {
		t.Fatalf("legacy timestamps mismatch: start=%d end=%d", generation.StartUnixNano, generation.EndUnixNano)
	}
}

func TestExporterReadsLegacyTraceRequestSchemaWithoutModelingColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	createLegacyScribeSchema(t, db)

	insertLegacySession(t, db, "sess_legacy", 1710000000000, 1710000005000)
	insertLegacyTurn(t, db, "turn_legacy", "sess_legacy", 1710000000100, 1710000004000)
	insertLegacyTraceRequest(t, db, "req_legacy", "sess_legacy", "turn_legacy", "call_legacy", "request", "anthropic", "claude-requested", 1710000000200)
	insertLegacyTraceRequest(t, db, "resp_legacy", "sess_legacy", "turn_legacy", "call_legacy", "response", "anthropic", "claude-response", 1710000001200)
	insertRawPayload(t, db, "raw_req_legacy", "req_legacy", "identity", []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":512}`))
	insertRawPayload(t, db, "raw_resp_legacy", "resp_legacy", "identity", []byte(`{"model":"claude-response","stop_reason":"end_turn","content":[{"type":"text","text":"world"}],"usage":{"input_tokens":7,"output_tokens":8,"cache_read_input_tokens":2}}`))

	result, err := Export(context.Background(), Options{DBPath: dbPath})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	generation := findSpanByAttr(t, result.Spans, "langfuse.observation.type", "generation")
	assertAttr(t, generation.Attributes, "gen_ai.request.model", "claude-requested")
	assertAttr(t, generation.Attributes, "gen_ai.response.model", "claude-response")
	assertAttr(t, generation.Attributes, "gen_ai.usage.input_tokens", int64(7))
	assertAttr(t, generation.Attributes, "gen_ai.usage.cache_read.input_tokens", int64(2))
	assertAttr(t, generation.Attributes, "gen_ai.request.max_tokens", int64(512))
}

func findSpanByAttr(t *testing.T, spans []Span, key string, value any) Span {
	t.Helper()
	for _, span := range spans {
		if got, ok := span.Attributes[key]; ok && equalAttr(got, value) {
			return span
		}
	}
	t.Fatalf("span with %s=%#v not found in %#v", key, value, spans)
	return Span{}
}

func findSpanByAttrs(t *testing.T, spans []Span, attrs map[string]any) Span {
	t.Helper()
	for _, span := range spans {
		matches := true
		for key, value := range attrs {
			if got, ok := span.Attributes[key]; !ok || !equalAttr(got, value) {
				matches = false
				break
			}
		}
		if matches {
			return span
		}
	}
	t.Fatalf("span with attrs %#v not found in %#v", attrs, spans)
	return Span{}
}

func assertAttr(t *testing.T, attrs map[string]any, key string, want any) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Fatalf("missing attr %s in %#v", key, attrs)
	}
	if !equalAttr(got, want) {
		t.Fatalf("attr %s mismatch: got %#v want %#v", key, got, want)
	}
}

func assertAttrJSONContains(t *testing.T, attrs map[string]any, key string, want string) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Fatalf("missing attr %s in %#v", key, attrs)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("attr %s is not JSON-marshalable: %v", key, err)
	}
	if !strings.Contains(string(gotJSON), want) {
		t.Fatalf("attr %s should contain %q, got %s", key, want, gotJSON)
	}
}

func equalAttr(got any, want any) bool {
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	return string(gotJSON) == string(wantJSON)
}

func ptr(v int64) *int64 { return &v }

type traceRow struct {
	ID                  string
	SessionID           string
	TurnID              string
	ParentRequestID     string
	RequestID           string
	Direction           string
	Provider            string
	Model               string
	RequestedModel      string
	Timestamp           int64
	Summary             string
	Outcome             string
	HTTPStatus          int64
	StopReason          string
	InputTokens         *int64
	OutputTokens        *int64
	ToolCallCount       int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64
	ReasoningTokens     *int64
	MaxTokens           *int64
	DurationMS          *int64
}

func createScribeSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, name TEXT, is_starred INTEGER NOT NULL DEFAULT 0, started_at INTEGER NOT NULL, ended_at INTEGER, metadata TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE turns (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, turn_number INTEGER NOT NULL, status TEXT NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER, abort_reason TEXT)`,
		`CREATE TABLE trace_requests (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, turn_id TEXT NOT NULL, parent_request_id TEXT,
			request_id TEXT NOT NULL, direction TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
			requested_model TEXT, timestamp INTEGER NOT NULL, summary TEXT NOT NULL DEFAULT '{}',
			outcome TEXT NOT NULL DEFAULT 'ok', error_type TEXT, error_message TEXT, http_status INTEGER, stop_reason TEXT,
			input_tokens INTEGER, output_tokens INTEGER, tool_call_count INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER, cache_creation_tokens INTEGER, reasoning_tokens INTEGER,
			max_tokens INTEGER, duration_ms INTEGER
		)`,
		`CREATE TABLE raw_payloads (id TEXT PRIMARY KEY, trace_request_id TEXT NOT NULL UNIQUE, content_type TEXT NOT NULL DEFAULT 'application/json', content_encoding TEXT NOT NULL DEFAULT 'identity', body BLOB, size_bytes INTEGER NOT NULL DEFAULT 0)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
}

func insertSession(t *testing.T, db *sql.DB, id string, source string, start int64, end int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (id, source, name, started_at, ended_at, metadata) VALUES (?, ?, ?, ?, ?, ?)`, id, source, id, start, end, "{}"); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func insertTurn(t *testing.T, db *sql.DB, id string, sessionID string, number int64, status string, start int64, end int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO turns (id, session_id, turn_number, status, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?)`, id, sessionID, number, status, start, end); err != nil {
		t.Fatalf("insert turn: %v", err)
	}
}

func insertTraceRequest(t *testing.T, db *sql.DB, row traceRow) {
	t.Helper()
	if row.Outcome == "" {
		row.Outcome = "ok"
	}
	if row.Summary == "" {
		row.Summary = "{}"
	}
	if _, err := db.Exec(`INSERT INTO trace_requests (
		id, session_id, turn_id, parent_request_id, request_id, direction, provider, model,
		requested_model, timestamp, summary, outcome, http_status, stop_reason, input_tokens,
		output_tokens, tool_call_count, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
		max_tokens, duration_ms
	) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, 0), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.SessionID, row.TurnID, row.ParentRequestID, row.RequestID, row.Direction,
		row.Provider, row.Model, row.RequestedModel, row.Timestamp, row.Summary, row.Outcome, row.HTTPStatus,
		row.StopReason, row.InputTokens, row.OutputTokens, row.ToolCallCount, row.CacheReadTokens,
		row.CacheCreationTokens, row.ReasoningTokens, row.MaxTokens, row.DurationMS,
	); err != nil {
		t.Fatalf("insert trace request %s: %v", row.ID, err)
	}
}

func insertRawPayload(t *testing.T, db *sql.DB, id string, traceRequestID string, encoding string, body []byte) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO raw_payloads (id, trace_request_id, content_type, content_encoding, body, size_bytes) VALUES (?, ?, 'application/json', ?, ?, ?)`, id, traceRequestID, encoding, body, len(body)); err != nil {
		t.Fatalf("insert raw payload: %v", err)
	}
}

func createLegacyScribeSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, name TEXT, is_starred INTEGER NOT NULL DEFAULT 0, started_at INTEGER NOT NULL, ended_at INTEGER, metadata TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE turns (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, turn_number INTEGER NOT NULL, status TEXT NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER, abort_reason TEXT)`,
		`CREATE TABLE trace_requests (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, turn_id TEXT NOT NULL, parent_request_id TEXT,
			request_id TEXT NOT NULL, direction TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
			timestamp INTEGER NOT NULL, summary TEXT NOT NULL DEFAULT '{}',
			outcome TEXT NOT NULL DEFAULT 'ok', error_type TEXT, error_message TEXT, http_status INTEGER, stop_reason TEXT,
			input_tokens INTEGER, output_tokens INTEGER, duration_ms INTEGER
		)`,
		`CREATE TABLE raw_payloads (id TEXT PRIMARY KEY, trace_request_id TEXT NOT NULL UNIQUE, content_type TEXT NOT NULL DEFAULT 'application/json', content_encoding TEXT NOT NULL DEFAULT 'identity', body BLOB, size_bytes INTEGER NOT NULL DEFAULT 0)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create legacy schema: %v", err)
		}
	}
}

func insertLegacySession(t *testing.T, db *sql.DB, id string, start int64, end int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (id, source, name, started_at, ended_at, metadata) VALUES (?, 'codex', ?, ?, ?, '{}')`, id, id, start, end); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
}

func insertLegacyTurn(t *testing.T, db *sql.DB, id string, sessionID string, start int64, end int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO turns (id, session_id, turn_number, status, started_at, ended_at) VALUES (?, ?, 1, 'completed', ?, ?)`, id, sessionID, start, end); err != nil {
		t.Fatalf("insert legacy turn: %v", err)
	}
}

func insertLegacyTraceRequest(t *testing.T, db *sql.DB, id string, sessionID string, turnID string, requestID string, direction string, providerName string, model string, timestamp int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO trace_requests (id, session_id, turn_id, request_id, direction, provider, model, timestamp, summary) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}')`, id, sessionID, turnID, requestID, direction, providerName, model, timestamp); err != nil {
		t.Fatalf("insert legacy trace request %s: %v", id, err)
	}
}
