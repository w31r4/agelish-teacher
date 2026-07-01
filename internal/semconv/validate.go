package semconv

import (
	"encoding/json"
	"fmt"

	"github.com/zenfun/agelish-teacher/internal/otel"
)

type Finding struct {
	SpanID  string `json:"span_id"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}

func ValidateSpans(spans []otel.Span) []Finding {
	var findings []Finding
	for _, span := range spans {
		if span.Attributes["langfuse.observation.type"] == "generation" {
			findings = append(findings, validateGenerationSpan(span)...)
		}
		if span.Attributes["langfuse.observation.type"] == "agent" {
			findings = append(findings, validateAgentSpan(span)...)
		}
		if span.Attributes["gen_ai.operation.name"] == "execute_tool" {
			findings = append(findings, validateToolSpan(span)...)
		}
	}
	return findings
}

func validateGenerationSpan(span otel.Span) []Finding {
	var findings []Finding
	if span.Kind != "SPAN_KIND_CLIENT" {
		findings = append(findings, Finding{
			SpanID:  span.SpanID,
			Message: "generation span kind should be SPAN_KIND_CLIENT",
		})
	}
	for _, key := range []string{
		"gen_ai.provider.name",
		"gen_ai.operation.name",
		"langfuse.session.id",
		"langfuse.observation.input",
		"langfuse.observation.output",
	} {
		if isEmpty(span.Attributes[key]) {
			findings = append(findings, missing(span, key))
		}
	}
	for _, key := range []string{
		"gen_ai.input.messages",
		"gen_ai.output.messages",
	} {
		if value, ok := span.Attributes[key]; ok && !validJSONString(value) {
			findings = append(findings, Finding{
				SpanID:  span.SpanID,
				Key:     key,
				Message: fmt.Sprintf("invalid JSON in %s", key),
			})
		}
	}
	return findings
}

func validateAgentSpan(span otel.Span) []Finding {
	var findings []Finding
	if span.Kind != "SPAN_KIND_INTERNAL" {
		findings = append(findings, Finding{
			SpanID:  span.SpanID,
			Message: "agent span kind should be SPAN_KIND_INTERNAL",
		})
	}
	for _, key := range []string{
		"gen_ai.operation.name",
		"langfuse.session.id",
		"scribe.request_role",
	} {
		if isEmpty(span.Attributes[key]) {
			findings = append(findings, missing(span, key))
		}
	}
	if span.ParentSpanID == "" {
		findings = append(findings, Finding{
			SpanID:  span.SpanID,
			Message: "agent span should have a parent turn or agent span",
		})
	}
	return findings
}

func validateToolSpan(span otel.Span) []Finding {
	var findings []Finding
	for _, key := range []string{
		"gen_ai.tool.name",
		"gen_ai.tool.call.id",
		"langfuse.observation.input",
		"langfuse.observation.output",
	} {
		if isEmpty(span.Attributes[key]) {
			findings = append(findings, missing(span, key))
		}
	}
	if span.ParentSpanID == "" {
		findings = append(findings, Finding{
			SpanID:  span.SpanID,
			Message: "execute_tool span should have a parent generation span",
		})
	}
	return findings
}

func missing(span otel.Span, key string) Finding {
	return Finding{
		SpanID:  span.SpanID,
		Key:     key,
		Message: "missing " + key,
	}
}

func isEmpty(value any) bool {
	switch got := value.(type) {
	case nil:
		return true
	case string:
		return got == ""
	default:
		return false
	}
}

func validJSONString(value any) bool {
	text, ok := value.(string)
	if !ok || text == "" {
		return false
	}
	var decoded any
	return json.Unmarshal([]byte(text), &decoded) == nil
}
