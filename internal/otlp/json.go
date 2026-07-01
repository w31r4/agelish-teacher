package otlp

import (
	"sort"
	"strconv"

	"github.com/zenfun/agelish-teacher/internal/jsonx"
	"github.com/zenfun/agelish-teacher/internal/otel"
)

type TracePayload struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

type Resource struct {
	Attributes []Attribute `json:"attributes,omitempty"`
}

type ScopeSpans struct {
	Scope Scope  `json:"scope"`
	Spans []Span `json:"spans"`
}

type Scope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type Span struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId,omitempty"`
	Name              string      `json:"name"`
	Kind              string      `json:"kind"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []Attribute `json:"attributes,omitempty"`
	Status            Status      `json:"status,omitempty"`
}

type Status struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Attribute struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

type AnyValue struct {
	StringValue string      `json:"stringValue,omitempty"`
	IntValue    string      `json:"intValue,omitempty"`
	DoubleValue *float64    `json:"doubleValue,omitempty"`
	BoolValue   *bool       `json:"boolValue,omitempty"`
	ArrayValue  *ArrayValue `json:"arrayValue,omitempty"`
}

type ArrayValue struct {
	Values []AnyValue `json:"values"`
}

func BuildTracePayload(spans []otel.Span) TracePayload {
	otlpSpans := make([]Span, 0, len(spans))
	for _, span := range spans {
		otlpSpans = append(otlpSpans, Span{
			TraceID:           span.TraceID,
			SpanID:            span.SpanID,
			ParentSpanID:      span.ParentSpanID,
			Name:              span.Name,
			Kind:              span.Kind,
			StartTimeUnixNano: strconv.FormatInt(span.StartUnixNano, 10),
			EndTimeUnixNano:   strconv.FormatInt(span.EndUnixNano, 10),
			Attributes:        attributes(span.Attributes),
			Status: Status{
				Code:    span.Status.Code,
				Message: span.Status.Message,
			},
		})
	}
	return TracePayload{
		ResourceSpans: []ResourceSpans{{
			Resource: Resource{Attributes: attributes(map[string]any{
				"service.name":           "agelish-teacher",
				"telemetry.sdk.language": "go",
				"telemetry.sdk.name":     "agelish-teacher",
			})},
			ScopeSpans: []ScopeSpans{{
				Scope: Scope{Name: "agelish-teacher"},
				Spans: otlpSpans,
			}},
		}},
	}
}

func attributes(attrs map[string]any) []Attribute {
	if len(attrs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Attribute, 0, len(keys))
	for _, key := range keys {
		result = append(result, Attribute{Key: key, Value: anyValue(attrs[key])})
	}
	return result
}

func anyValue(value any) AnyValue {
	switch got := value.(type) {
	case string:
		return AnyValue{StringValue: got}
	case int:
		return AnyValue{IntValue: strconv.FormatInt(int64(got), 10)}
	case int64:
		return AnyValue{IntValue: strconv.FormatInt(got, 10)}
	case int32:
		return AnyValue{IntValue: strconv.FormatInt(int64(got), 10)}
	case float64:
		return AnyValue{DoubleValue: &got}
	case float32:
		value64 := float64(got)
		return AnyValue{DoubleValue: &value64}
	case bool:
		return AnyValue{BoolValue: &got}
	case []string:
		values := make([]AnyValue, 0, len(got))
		for _, item := range got {
			values = append(values, AnyValue{StringValue: item})
		}
		return AnyValue{ArrayValue: &ArrayValue{Values: values}}
	case []int64:
		values := make([]AnyValue, 0, len(got))
		for _, item := range got {
			values = append(values, AnyValue{IntValue: strconv.FormatInt(item, 10)})
		}
		return AnyValue{ArrayValue: &ArrayValue{Values: values}}
	default:
		return AnyValue{StringValue: jsonx.String(value)}
	}
}
