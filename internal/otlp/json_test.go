package otlp

import (
	"encoding/json"
	"testing"

	"github.com/zenfun/agelish-teacher/internal/otel"
)

func TestMarshalTracePayloadProducesOTLPHTTPJSONShape(t *testing.T) {
	payload := BuildTracePayload([]otel.Span{{
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "0123456789abcdef",
		ParentSpanID:  "1111111111111111",
		Name:          "anthropic claude",
		Kind:          "SPAN_KIND_CLIENT",
		StartUnixNano: 1710000000000000000,
		EndUnixNano:   1710000001000000000,
		Attributes: map[string]any{
			"gen_ai.provider.name":           "anthropic",
			"gen_ai.usage.input_tokens":      int64(10),
			"gen_ai.response.finish_reasons": []string{"stop_sequence"},
		},
	}})

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	resourceSpans, ok := decoded["resourceSpans"].([]any)
	if !ok || len(resourceSpans) != 1 {
		t.Fatalf("resourceSpans missing: %#v", decoded)
	}
	span := resourceSpans[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
	if span["traceId"] != "0123456789abcdef0123456789abcdef" || span["spanId"] != "0123456789abcdef" {
		t.Fatalf("ids missing in span: %#v", span)
	}
	if span["kind"] != "SPAN_KIND_CLIENT" {
		t.Fatalf("kind mismatch: %#v", span)
	}
}
