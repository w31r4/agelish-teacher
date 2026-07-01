# Agelish Teacher

Agelish Teacher is a standalone Go binary that translates raw LLM provider HTTP
traffic into OpenTelemetry GenAI spans for OTLP endpoints such as Langfuse.

The core is a raw-body translator: given the request/response bodies of a
provider HTTP call (Anthropic Messages, OpenAI/Codex Responses — JSON or SSE),
it projects them into `gen_ai.*` spans and attributes. In principle any captured
HTTP raw body from a supported provider can be fed through this mapping; it is
not tied to Scribe. Scribe's SQLite trace database is simply the current
ingestion source — it supplies the raw bodies (plus session/turn structure) that
the translator consumes.

The converter is intentionally decoupled from any single capture tool. Scribe
keeps faithful wire capture; this repo owns the experimental OTel GenAI mapping
churn and can be pointed at other raw-body sources over time.

## Current Scope

- Reads Scribe tables: `sessions`, `turns`, `trace_requests`, `raw_payloads`.
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
