## Context

Agelish exports Scribe request/response pairs as OTel spans with Langfuse observation attributes. Provider parsers currently include system prompts in `InputMessages`, and the exporter uses those full messages for both forensic attributes and Langfuse display input. In long Codex/Claude sessions this makes each generation hard to inspect because the repeated system prompt dominates the Langfuse input panel.

The repo also lacks a local `.agent` reminder that mapping changes must be verified with real Scribe data and re-sent to Langfuse.

## Goals / Non-Goals

**Goals:**
- Keep full forensic request data available in OTel attributes.
- Make Langfuse display input concise after the first generation by filtering repeated system messages from `langfuse.observation.input`.
- Keep system prompts available through `gen_ai.system_instructions`.
- Add repo-local agent guidance requiring real-data Langfuse regression after mapping changes.

**Non-Goals:**
- Do not change Scribe capture or storage.
- Do not delete system prompts from `gen_ai.input.messages`.
- Do not introduce a new CLI flag for this behavior in this change.

## Decisions

- Use display-only filtering in the exporter rather than provider parsers. Provider parsers should continue returning complete request messages for forensic attributes and tests.
- Treat the first generation in each exported session as the context-bearing generation; it may include system prompts in `langfuse.observation.input`. Later generation and agent observation display inputs omit `role=system`.
- Keep `gen_ai.system_instructions` on every generation with system instructions so the prompt is still searchable in metadata.
- If filtering removes all display input messages, emit a small diagnostic object rather than leaving Langfuse input empty.
- Add `.agent/rules/agelish-langfuse-regression.md` in this repo because OpenSpec scopes edits to the Agelish project.

## Risks / Trade-offs

- Filtering only Langfuse display input means `gen_ai.input.messages` can still be large in raw attributes. This is intentional for audit fidelity.
- First-generation detection is exporter-local and deterministic by session order. If a session has no successful first generation, the first model observation still carries full context.
- Langfuse UI behavior can change, so real-data API verification remains required after changes.
