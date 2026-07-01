## Why

Langfuse generation input is currently too noisy for multi-turn agent traces because repeated system prompts dominate each observation. Users need the current turn's useful input visible by default, while system prompts remain available for audit.

Agelish also needs a durable agent instruction that every Langfuse/OTel mapping change must be validated against real Scribe data and re-sent to Langfuse, not only unit-tested.

## What Changes

- Make `langfuse.observation.input` suppress repeated system/developer prompt messages after the first generation in a session.
- Preserve system prompts in metadata-style attributes such as `gen_ai.system_instructions` for every generation where they are present.
- Keep forensic fields such as `gen_ai.input.messages` complete so raw OTel evidence is not lost.
- Add an Agelish `.agent` rule requiring real Scribe trace re-export, `-check-standard`, Langfuse send, and Langfuse API verification after every OTel/Langfuse mapping update.

## Capabilities

### New Capabilities
- `langfuse-observation-display`: Controls Langfuse-facing display input/output behavior for Agelish OTLP exports and defines required real-data regression verification.

### Modified Capabilities
- None.

## Impact

- Affects Agelish exporter mapping logic and tests.
- Adds repo-local agent workflow guidance under `.agent`.
- Does not change Scribe capture, raw payload storage, or provider parsing contracts.
