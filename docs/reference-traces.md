# Reference Langfuse Trace Shapes

This document records the external references used to standardize Agelish
Teacher's Langfuse mapping. It is a comparison log, not an Agelish output
contract by itself.

## Sources Checked

- Langfuse official repository:
  `/home/zenfun/workspace/langfuse`
- Langfuse official framework trace fixtures:
  `/home/zenfun/workspace/langfuse/worker/src/__tests__/chatml/framework-traces`
- Langfuse official Claude Code Observability Plugin:
  `/home/zenfun/workspace/Claude-Observability-Plugin`
- Third-party Claude Code template used as a secondary implementation check:
  `/home/zenfun/workspace/claude-code-langfuse-template`

The official framework fixture README says these traces are generated from
Langfuse's documented framework integration examples and are used by Langfuse
tests. They are the strongest local source for known-good Langfuse observation
shape.

## Fixture Observations

Selected official fixture files with agent/tool structure:

| Fixture | Observation Types | Input/Output Shape |
| --- | --- | --- |
| `openai-agents-2025-09-30.trace.json` | `AGENT 2`, `GENERATION 2`, `TOOL 1` | generations and tool have input/output; agent nodes do not |
| `pydantic-ai-tools-2025-12-04.trace.json` | `SPAN 5`, `GENERATION 4`, `TOOL 3` | root workflow span, generations, and tools have input/output |
| `microsoft-agent-2025-12-17.trace.json` | `AGENT 1`, `GENERATION 2`, `TOOL 1` | agent, generations, and tool have input/output |
| `claude-agent-2025-12-22.trace.json` | `SPAN 1`, `GENERATION 4`, `TOOL 2` | conversation span, generations, and tools have input/output |

The stable cross-source rule is:

- Generation observations must carry readable input and output.
- Tool observations must carry readable input and output.
- Root workflow/turn observations should carry input and output when the
  framework exposes them.
- Agent observations vary by framework; Agelish fills them when wrapping a
  single subagent generation because the paired request/response is known.

## Claude Code Reference

Langfuse's official Claude Code plugin reads Claude Code JSONL transcripts
incrementally on each `Stop` hook and emits:

- one Langfuse trace per Claude Code turn;
- a root span named `Turn N` with user input and final assistant output;
- one generation per assistant message;
- tool observations nested under the generation that emitted the tool call;
- tool observation input from the `tool_use` block;
- tool observation output from the matching `tool_result` block;
- the previous batch of tool results as the next generation's input.

Agelish Teacher is an offline Scribe database translator, so it keeps one OTel
trace per Scribe session and one child span per Scribe turn. To match the same
display semantics, it still emits:

- `langfuse.trace.input` and `langfuse.trace.output` on the session root;
- `langfuse.observation.input` and `langfuse.observation.output` on each turn;
- `langfuse.observation.input` and `langfuse.observation.output` on each
  generation;
- `langfuse.observation.input` and `langfuse.observation.output` on each tool
  span, pairing tool calls with later tool results in the same Scribe turn.

If no matching result was captured for a tool call, Agelish Teacher emits a
diagnostic `missing_tool_result` observation output rather than inventing a tool
return value.

## Local Claude Code Plugin Experiment

An isolated synthetic Claude Code transcript was run through the official
Langfuse Claude Code Observability Plugin with:

- temporary home: `/home/zenfun/workspace/agelish-teacher/tmp/claude-home`
- transcript session id: `claude-exp-1`
- local Langfuse trace id: `17e8a9acfc1c74705311e94904d2d2d1`

The Langfuse public API reported:

- trace input/output present;
- observations: `SPAN 1`, `GENERATION 2`, `TOOL 1`;
- every span, generation, and tool had non-null input/output;
- the tool observation was nested under the first generation;
- the second generation input reflected the previous tool result batch.

## Local Comparison Commands

Count observation types and input/output coverage in official fixtures:

```bash
python - <<'PY'
import json
from collections import Counter, defaultdict
from pathlib import Path

base = Path("/home/zenfun/workspace/langfuse/worker/src/__tests__/chatml/framework-traces")
for p in sorted(base.glob("*.trace.json")):
    data = json.loads(p.read_text())
    rows = data if isinstance(data, list) else data.get("observations") or data.get("data") or []
    counts = Counter((r.get("type") or "UNKNOWN") for r in rows)
    if not counts.get("TOOL") and not counts.get("AGENT"):
        continue
    coverage = defaultdict(lambda: [0, 0, 0])
    for r in rows:
        typ = r.get("type") or "UNKNOWN"
        coverage[typ][0] += 1
        coverage[typ][1] += r.get("input") is not None
        coverage[typ][2] += r.get("output") is not None
    print(p.name, dict(counts), dict(coverage))
PY
```
