# Agelish Langfuse Regression

Apply this rule whenever changing Agelish OTel, GenAI semantic convention, provider-to-OTLP, or Langfuse observation mapping behavior.

After every mapping update:

1. Run the relevant unit tests and `go test ./...`.
2. Re-export real Scribe data with `-check-standard`; prefer session `019f1d0de95d71ccb36bef8a646e070d` when validating Codex goal and multi-turn behavior.
3. Send the resulting OTLP payload to local Langfuse.
4. Verify the trace through the Langfuse API, not only by inspecting the local JSON file.

Recommended commands:

```bash
rtk go test ./...
rtk go run ./cmd/agelish-teacher \
  -db /home/zenfun/.scribe/traces.db \
  -session 019f1d0de95d71ccb36bef8a646e070d \
  -include-active \
  -format otlp-json \
  -check-standard \
  -out /tmp/agelish-langfuse-regression.otlp.json
rtk go run ./cmd/agelish-teacher \
  -db /home/zenfun/.scribe/traces.db \
  -session 019f1d0de95d71ccb36bef8a646e070d \
  -include-active \
  -format otlp-json \
  -check-standard \
  -out /tmp/agelish-langfuse-regression-send.otlp.json \
  -send \
  -langfuse-url http://localhost:3000 \
  -langfuse-public-key pk-lf-agelish-local \
  -langfuse-secret-key sk-lf-agelish-local
```

If a raw HTTP body lacks enough context to validate metadata or session-level behavior, use the Scribe DB export path. Transport errors may legitimately have no model output; validate them through OTel status, `error.type`, and Langfuse diagnostic output fields.
