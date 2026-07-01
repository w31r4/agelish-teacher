# Agelish Teacher

Agelish Teacher is a standalone Go binary that translates raw LLM provider HTTP
traffic into OpenTelemetry GenAI spans for OTLP endpoints such as Langfuse.

The core input is a canonical raw HTTP envelope: captured request/response
metadata plus the raw body bytes. Provider parsers then map Anthropic Messages,
OpenAI-compatible Chat Completions, and OpenAI/Codex Responses JSON/SSE into
`gen_ai.*` spans and attributes. It is not tied to Scribe. Scribe's SQLite trace
database is one importer that supplies raw bodies plus session/turn structure.

The converter is intentionally decoupled from any single capture tool. Scribe
keeps faithful wire capture; this repo owns the experimental OTel GenAI mapping
churn and can be pointed at other raw-body sources over time.

## Current Scope

- Reads Scribe tables: `sessions`, `turns`, `trace_requests`, `raw_payloads`.
- Reads canonical raw HTTP envelope JSONL from `-raw-envelope` or
  `-raw-envelope-stdin`, without Scribe.
- Keeps the older single-pair convenience flags `-raw-provider`, `-raw-request`,
  and `-raw-response`; internally these are wrapped into raw HTTP envelopes.
- Decodes raw payloads stored as `identity` or `zstd`.
- Reconstructs deterministic trace/span IDs from Scribe primary keys.
- Emits one session span, one span per turn, one `SPAN_KIND_CLIENT` generation
  span per response `TraceRequest`, plus `execute_tool` child spans.
- Maps Anthropic Messages API JSON/SSE and OpenAI/Codex Responses JSON/SSE into
  `gen_ai.input.messages`, `gen_ai.output.messages`, token usage, finish reasons,
  model fields, and tool-call attributes.
- Emits Langfuse trace/observation input and output at session, turn,
  generation, and tool levels; tool outputs are paired from later tool-result
  payloads in the same Scribe turn.
- Emits OTLP HTTP/JSON payloads and can POST them to Langfuse.
- Includes `-check-standard` to compare generated spans against this repo's
  GenAI OTel profile before sending.

## Build And Test

```bash
go test ./...
go build ./cmd/agelish-teacher
```

## Dry Run

```bash
go run ./cmd/agelish-teacher \
  -db ~/.scribe/traces.db \
  -format otlp-json \
  -check-standard \
  -out tmp/scribe-otlp.json
```

Use `-format spans` when debugging Agelish Teacher's internal span model before
OTLP JSON conversion.

## Raw HTTP Envelope Mode

Use raw envelope mode when another process has captured HTTP request/response
traffic and can provide correlation metadata:

```jsonl
{"source":"reasonix","request_id":"call-1","session_id":"sess-1","turn_id":"turn-1","direction":"request","method":"POST","url":"https://api.example.test/v1/chat/completions","headers":{"content-type":"application/json"},"body":"{\"model\":\"deepseek-v4-pro\",\"messages\":[{\"role\":\"user\",\"content\":\"Say ok.\"}]}","timestamp_ms":1710000000200}
{"source":"reasonix","request_id":"call-1","session_id":"sess-1","turn_id":"turn-1","direction":"response","status_code":200,"headers":{"content-type":"application/json"},"body_text":"{\"model\":\"deepseek-v4-pro\",\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}","timestamp_ms":1710000001200}
```

```bash
go run ./cmd/agelish-teacher \
  -raw-envelope /path/to/envelopes.jsonl \
  -format otlp-json \
  -check-standard \
  -out tmp/raw-envelope-otlp.json
```

The body can be supplied as `body`/`body_text` for UTF-8 JSON or SSE. Use
`body_base64` for non-text bytes. If `provider` is omitted, Agelish infers
OpenAI-compatible traffic from paths such as `/v1/chat/completions`; `source`
is preserved for display, so a Reasonix OpenAI-compatible call appears as
Reasonix while still using the OpenAI parser.

## Raw Body Pair Mode

Use raw body mode when another process already has provider HTTP bodies and only
needs Agelish Teacher for parsing and OTLP projection. This is a compatibility
wrapper around raw envelope mode:

```bash
go run ./cmd/agelish-teacher \
  -raw-provider codex \
  -raw-request /path/to/request-body.json \
  -raw-response /path/to/response-body.json-or-sse \
  -raw-session-id external-session-1 \
  -format otlp-json \
  -check-standard \
  -out tmp/raw-otlp.json
```

Raw body pair mode still needs minimal correlation metadata. If
`-raw-session-id` or `-raw-request-id` are omitted, Agelish Teacher generates
local fallback IDs. For multi-turn trees, prefer raw envelope mode because it
can carry session, turn, request, timestamp, URL, header, and status metadata.

## Send To Langfuse

```bash
export LANGFUSE_BASE_URL=http://localhost:3000
export LANGFUSE_PUBLIC_KEY=pk-lf-...
export LANGFUSE_SECRET_KEY=sk-lf-...

go run ./cmd/agelish-teacher \
  -db ~/.scribe/traces.db \
  -check-standard \
  -send
```

The client posts to `/api/public/otel/v1/traces` with Basic auth and
`x-langfuse-ingestion-version: 4`.

## Notes

OpenTelemetry GenAI semantic conventions are still development-stage and have
already moved out of the main OTel semconv docs. The `semconv` package is a
local executable profile, not a claim of complete conformance.

See `docs/reference-traces.md` for the Langfuse official fixtures and Claude
Code plugin used as comparison sources for this profile.
