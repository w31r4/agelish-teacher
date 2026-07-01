package semconv

import (
	"strings"
	"testing"

	"github.com/zenfun/agelish-teacher/internal/otel"
)

func TestValidateAcceptsCompleteGenerationAndToolSpans(t *testing.T) {
	findings := ValidateSpans([]otel.Span{
		{
			SpanID: "gen1",
			Kind:   "SPAN_KIND_CLIENT",
			Attributes: map[string]any{
				"langfuse.observation.type":                "generation",
				"langfuse.session.id":                      "sess_1",
				"gen_ai.operation.name":                    "chat",
				"gen_ai.provider.name":                     "anthropic",
				"gen_ai.request.model":                     "claude-3-5-sonnet",
				"gen_ai.response.model":                    "claude-3-5-sonnet-20241022",
				"gen_ai.input.messages":                    `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]`,
				"gen_ai.output.messages":                   `[{"role":"assistant","parts":[{"type":"text","content":"hello"}]}]`,
				"langfuse.observation.input":               []map[string]any{{"role": "user", "content": "hi"}},
				"langfuse.observation.output":              []map[string]any{{"role": "assistant", "content": "hello"}},
				"gen_ai.usage.input_tokens":                int64(10),
				"gen_ai.usage.output_tokens":               int64(20),
				"gen_ai.response.finish_reasons":           []string{"stop_sequence"},
				"gen_ai.usage.cache_read.input_tokens":     int64(2),
				"gen_ai.usage.reasoning.output_tokens":     int64(3),
				"gen_ai.usage.cache_creation.input_tokens": int64(1),
			},
		},
		{
			SpanID:       "agent1",
			ParentSpanID: "turn1",
			Kind:         "SPAN_KIND_INTERNAL",
			Attributes: map[string]any{
				"langfuse.observation.type": "agent",
				"langfuse.session.id":       "sess_1",
				"gen_ai.operation.name":     "invoke_agent",
				"scribe.request_role":       "subagent",
			},
		},
		{
			SpanID:       "tool1",
			ParentSpanID: "gen1",
			Kind:         "SPAN_KIND_INTERNAL",
			Attributes: map[string]any{
				"gen_ai.operation.name": "execute_tool",
				"gen_ai.tool.name":      "calculator",
				"gen_ai.tool.call.id":   "toolu_1",
				"langfuse.observation.input": map[string]any{
					"expression": "2+2",
				},
				"langfuse.observation.output": "4",
			},
		},
	})
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestValidateReportsMissingRequiredGenerationAttributes(t *testing.T) {
	findings := ValidateSpans([]otel.Span{{
		SpanID: "gen1",
		Kind:   "SPAN_KIND_INTERNAL",
		Attributes: map[string]any{
			"langfuse.observation.type": "generation",
			"gen_ai.input.messages":     `not-json`,
		},
	}})

	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	joined := findingsText(findings)
	for _, want := range []string{
		"generation span kind should be SPAN_KIND_CLIENT",
		"missing gen_ai.provider.name",
		"missing gen_ai.operation.name",
		"invalid JSON in gen_ai.input.messages",
		"missing langfuse.session.id",
		"missing langfuse.observation.input",
		"missing langfuse.observation.output",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected finding %q in:\n%s", want, joined)
		}
	}
}

func TestValidateReportsMissingRequiredAgentAttributes(t *testing.T) {
	findings := ValidateSpans([]otel.Span{{
		SpanID: "agent1",
		Kind:   "SPAN_KIND_CLIENT",
		Attributes: map[string]any{
			"langfuse.observation.type": "agent",
		},
	}})

	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	joined := findingsText(findings)
	for _, want := range []string{
		"agent span kind should be SPAN_KIND_INTERNAL",
		"agent span should have a parent turn or agent span",
		"missing gen_ai.operation.name",
		"missing langfuse.session.id",
		"missing scribe.request_role",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected finding %q in:\n%s", want, joined)
		}
	}
}

func findingsText(findings []Finding) string {
	var b strings.Builder
	for _, finding := range findings {
		b.WriteString(finding.SpanID)
		b.WriteString(" ")
		b.WriteString(finding.Message)
		b.WriteString("\n")
	}
	return b.String()
}
