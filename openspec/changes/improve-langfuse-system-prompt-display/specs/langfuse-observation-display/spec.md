## ADDED Requirements

### Requirement: Generation display input filters repeated system prompts
Agelish SHALL preserve system prompts in forensic attributes while preventing repeated system prompt messages from dominating Langfuse generation display input.

#### Scenario: First generation keeps full display input
- **WHEN** Agelish exports the first model generation for a session and the parsed request includes a system message
- **THEN** `langfuse.observation.input` SHALL include that system message
- **AND** `gen_ai.system_instructions` SHALL contain the system prompt

#### Scenario: Later generations omit system messages from display input
- **WHEN** Agelish exports a later model generation for the same session and the parsed request includes a system message
- **THEN** `langfuse.observation.input` SHALL omit messages with `role=system`
- **AND** `gen_ai.system_instructions` SHALL still contain the system prompt
- **AND** `gen_ai.input.messages` SHALL remain complete

#### Scenario: Filtering leaves no display input
- **WHEN** system-message filtering removes every message that would have been shown in `langfuse.observation.input`
- **THEN** Agelish SHALL emit a diagnostic display input indicating that input was filtered and the system prompt remains available in metadata

### Requirement: Langfuse mapping changes require real-data regression
Agent instructions for this repository SHALL require every OTel/Langfuse mapping update to be validated with real Scribe data and sent to Langfuse.

#### Scenario: Agent updates mapping behavior
- **WHEN** an agent changes Agelish OTel or Langfuse mapping behavior
- **THEN** the agent SHALL run a real Scribe session export with `-check-standard`
- **AND** send the resulting OTLP payload to local Langfuse
- **AND** verify the resulting trace through the Langfuse API
