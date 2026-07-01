package semconv

import (
	"fmt"

	"github.com/zenfun/agelish-teacher/internal/jsonx"
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
	isError := isErrorGeneration(span)
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
	if isError {
		if span.Status.Code != "STATUS_CODE_ERROR" {
			findings = append(findings, Finding{
				SpanID:  span.SpanID,
				Message: "error generation span status should be STATUS_CODE_ERROR",
			})
		}
		if isEmpty(span.Attributes["error.type"]) {
			findings = append(findings, missing(span, "error.type"))
		}
	} else if isEmpty(span.Attributes["gen_ai.output.messages"]) {
		findings = append(findings, missing(span, "gen_ai.output.messages"))
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

func isErrorGeneration(span otel.Span) bool {
	if span.Status.Code == "STATUS_CODE_ERROR" {
		return true
	}
	if span.Attributes["langfuse.observation.level"] == "ERROR" {
		return true
	}
	for _, reason := range attrStringSlice(span.Attributes["gen_ai.response.finish_reasons"]) {
		if reason == "error" {
			return true
		}
	}
	return false
}

func attrStringSlice(value any) []string {
	switch got := value.(type) {
	case []string:
		return got
	case []any:
		values := make([]string, 0, len(got))
		for _, item := range got {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
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
	return jsonx.Unmarshal([]byte(text), &decoded) == nil
}
