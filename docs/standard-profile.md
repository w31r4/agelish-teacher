# GenAI OTel Profile

This is the comparison profile enforced by `agelish-teacher -check-standard`.
It is intentionally small and focused on fields Langfuse needs to render LLM
generations while staying close to OTel GenAI semantics.

Mapping priority:

1. Use standard OTel GenAI fields for model calls, usage, messages, tool calls,
   and tool results whenever they can represent the data.
2. Use generic/internal spans for Codex control traffic such as `/models`
   probes. These must not be labeled as GenAI chat generations.
3. Add Langfuse-specific fields only where OTel GenAI does not cover the
   Langfuse ingestion/display contract, for example observation type,
   session/trace binding, trace input/output, and observation input/output.

## Generation Span

Required:

- `kind = SPAN_KIND_CLIENT`
- `langfuse.observation.type = generation`
- `langfuse.session.id`
- `langfuse.observation.input`
- `langfuse.observation.output`
- `gen_ai.operation.name`
- `gen_ai.provider.name`

Validated when present:

- `gen_ai.input.messages` must be JSON.
- `gen_ai.output.messages` must be JSON.

Expected when available from Scribe:

- `gen_ai.request.model`
- `gen_ai.response.model`
- `gen_ai.usage.input_tokens`
- `gen_ai.usage.output_tokens`
- `gen_ai.usage.cache_read.input_tokens`
- `gen_ai.usage.cache_creation.input_tokens`
- `gen_ai.usage.reasoning.output_tokens`
- `gen_ai.request.max_tokens`
- `gen_ai.response.finish_reasons`
- `gen_ai.system_instructions`
- `scribe.request_role`
- `scribe.fine_role`
- `scribe.phase`
- `scribe.codex.turn_id`

## Agent Span

Required for `langfuse.observation.type = agent`:

- `kind = SPAN_KIND_INTERNAL`
- `parent_span_id` points at the turn or parent agent span.
- `langfuse.session.id`
- `gen_ai.operation.name = invoke_agent`
- `scribe.request_role`

Expected when available:

- `scribe.fine_role`
- `scribe.agent.type`
- `scribe.agent.subagent_kind`
- `scribe.codex.turn_id`
- `scribe.codex.thread_id`

## Tool Span

Required for `gen_ai.operation.name = execute_tool`:

- `parent_span_id` points at the generation span that emitted the tool call.
- `gen_ai.tool.name`
- `gen_ai.tool.call.id`
- `langfuse.observation.input`
- `langfuse.observation.output`

Expected when available:

- `gen_ai.tool.call.arguments`
- `gen_ai.tool.call.result`
- `gen_ai.tool.namespace`
- `scribe.request_role`
- `scribe.fine_role`

## Control Span

Codex control traffic, for example `control_kind = codex_model_probe` or
`path = /models?...`, is exported as `langfuse.observation.type = span`.

Control spans must not set:

- `langfuse.observation.type = generation`
- `gen_ai.operation.name = chat`

Expected when available:

- `scribe.control_kind`
- `scribe.phase`
- `scribe.agent.type`
- `scribe.path`
- `scribe.model_count`
- `langfuse.observation.input`
- `langfuse.observation.output`

## Trace And Turn Input/Output

Agelish Teacher emits Langfuse display payloads at three levels:

- Session root span:
  `langfuse.trace.input` and `langfuse.trace.output` are the first turn input
  and latest turn output observed in the Scribe session.
- Turn span:
  `langfuse.observation.input` and `langfuse.observation.output` are the first
  request input and latest response output observed in that Scribe turn.
- Generation span:
  `langfuse.observation.input` and `langfuse.observation.output` are the paired
  request payload and response payload for that model call.

Tool observations use the tool-call arguments as `langfuse.observation.input`
and the matching tool result as `langfuse.observation.output`. If Scribe captured
a tool call but no later matching result in the same turn, Agelish Teacher emits
`scribe.tool.result.status = missing` and a diagnostic
`{"status":"missing_tool_result"}` observation output instead of inventing a
tool result.

## References

- OTel GenAI attributes:
  https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/
- Langfuse OpenTelemetry integration:
  https://langfuse.com/integrations/native/opentelemetry
- Langfuse framework trace fixtures:
  https://github.com/langfuse/langfuse/tree/main/worker/src/__tests__/chatml/framework-traces
- Langfuse Claude Code Observability Plugin:
  https://github.com/langfuse/Claude-Observability-Plugin
- Local Langfuse Docker Compose:
  https://langfuse.com/self-hosting/deployment/docker-compose
