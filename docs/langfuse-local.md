# Local Langfuse Check

Use the official Langfuse Docker Compose deployment for local rendering checks.
The Langfuse docs currently recommend cloning their repository and running the
provided compose file for a local or VM deployment.

```bash
git clone https://github.com/langfuse/langfuse.git /home/zenfun/workspace/langfuse
cd /home/zenfun/workspace/langfuse
docker compose up
```

On this machine, localhost port `5432` was already in use, so the local stack was
started with a small ignored override in `/home/zenfun/workspace/langfuse`:

```yaml
services:
  postgres:
    ports: !reset []
```

When the web container reports ready, open:

```text
http://localhost:3000
```

Create a project in the UI, copy the public and secret keys, then send an
Agelish Teacher export:

```bash
cd /home/zenfun/workspace/agelish-teacher

export LANGFUSE_BASE_URL=http://localhost:3000
export LANGFUSE_PUBLIC_KEY=pk-lf-...
export LANGFUSE_SECRET_KEY=sk-lf-...

go run ./cmd/agelish-teacher \
  -db ~/.scribe/traces.db \
  -check-standard \
  -send
```

Agelish Teacher targets Langfuse v3's OTLP HTTP route:

```text
/api/public/otel/v1/traces
```

For large or sensitive local traces, first run:

```bash
go run ./cmd/agelish-teacher \
  -db ~/.scribe/traces.db \
  -format spans \
  -check-standard \
  -out tmp/spans.json
```

Expected rendering:

- One Langfuse trace per Scribe session.
- Session trace input/output populated from the first turn input and latest turn
  output.
- Turn spans populated with `langfuse.observation.input` and
  `langfuse.observation.output`.
- Generation observations for response-direction `TraceRequest` rows.
- Nested child observations for `execute_tool` spans.
- Generation prompt/completion content populated from
  `langfuse.observation.input`, `langfuse.observation.output`,
  `gen_ai.input.messages`, and `gen_ai.output.messages`.
- Tool input/output populated by pairing tool calls with later tool-result
  payloads in the same Scribe turn.
- Token usage populated from `gen_ai.usage.*` attributes when captured by Scribe.

## Local Validation Snapshot

Validated on Langfuse `3.202.1`:

- `GET /api/public/health` returned `{"status":"OK","version":"3.202.1"}`.
- OTLP POST with `x-langfuse-ingestion-version: 4` succeeded.
- Tool-heavy Codex trace `f2268b16f788bb47ad6b9f071227b72a` rendered
  `16` generations, `37` tools, and `2` spans. Every generation, tool, and span
  had non-null input/output in the Langfuse public API.
- In that tool-heavy trace, `33` tool observations had matched real tool
  results and `4` dangling tool calls rendered explicit `missing_tool_result`
  diagnostic outputs.
- Subagent trace `2fb2bf7f3c766453420afda3c0983ef3` rendered `2` agents,
  `2` generations, and `2` spans. Every agent, generation, and span had
  non-null input/output in the Langfuse public API.
