## 1. Tests First

- [x] 1.1 Add exporter tests proving the first generation display input keeps system messages.
- [x] 1.2 Add exporter tests proving later generation display input omits system messages while retaining `gen_ai.system_instructions` and complete `gen_ai.input.messages`.
- [x] 1.3 Add exporter tests proving filtered-empty display input emits a diagnostic object.

## 2. Exporter Implementation

- [x] 2.1 Track first model generation per exported session/raw export.
- [x] 2.2 Add display-input filtering for `langfuse.observation.input` without mutating forensic `gen_ai.input.messages`.
- [x] 2.3 Apply the same display-input policy to agent observations derived from generations.

## 3. Agent Guidance

- [x] 3.1 Add repo-local `.agent` guidance requiring real Scribe data re-export and Langfuse send after every OTel/Langfuse mapping change.
- [x] 3.2 Include the standard verification commands and recommended session `019f1d0de95d71ccb36bef8a646e070d`.

## 4. Verification

- [x] 4.1 Run targeted exporter tests.
- [x] 4.2 Run `go test ./...`.
- [x] 4.3 Validate the OpenSpec change.
- [x] 4.4 Re-export the real Codex goal session with `-check-standard`.
- [ ] 4.5 Send the re-exported trace to local Langfuse and verify via the Langfuse API that later generation display inputs no longer contain system prompts.
