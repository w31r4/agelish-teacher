package jsonx

import (
	"bytes"
	"io"
	"testing"
)

var benchmarkPayload = map[string]any{
	"resourceSpans": []any{
		map[string]any{
			"resource": map[string]any{
				"attributes": []any{
					map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "agelish-teacher"}},
				},
			},
			"scopeSpans": []any{
				map[string]any{
					"spans": []any{
						map[string]any{
							"traceId":           "0123456789abcdef0123456789abcdef",
							"spanId":            "0123456789abcdef",
							"name":              "reasonix - Turn 1",
							"kind":              2,
							"startTimeUnixNano": "1710000000200000000",
							"endTimeUnixNano":   "1710000001200000000",
							"attributes": []any{
								map[string]any{"key": "gen_ai.system", "value": map[string]any{"stringValue": "openai"}},
								map[string]any{"key": "gen_ai.input.messages", "value": map[string]any{"stringValue": `[{"role":"user","parts":[{"type":"text","content":"Say ok."}]}]`}},
								map[string]any{"key": "gen_ai.output.messages", "value": map[string]any{"stringValue": `[{"role":"assistant","parts":[{"type":"text","content":"ok"}]}]`}},
							},
						},
					},
				},
			},
		},
	},
}

var benchmarkRaw = []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"agelish-teacher"}}]},"scopeSpans":[{"spans":[{"traceId":"0123456789abcdef0123456789abcdef","spanId":"0123456789abcdef","name":"reasonix - Turn 1","kind":2,"startTimeUnixNano":"1710000000200000000","endTimeUnixNano":"1710000001200000000","attributes":[{"key":"gen_ai.system","value":{"stringValue":"openai"}},{"key":"gen_ai.input.messages","value":{"stringValue":"[{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"content\":\"Say ok.\"}]}]"}},{"key":"gen_ai.output.messages","value":{"stringValue":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"ok\"}]}]"}}]}]}]}]}`)

func BenchmarkMarshal(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Marshal(benchmarkPayload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshal(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var out map[string]any
		if err := Unmarshal(benchmarkRaw, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncoder(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if err := NewEncoder(io.Discard).Encode(benchmarkPayload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecoder(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var out map[string]any
		if err := NewDecoder(bytes.NewReader(benchmarkRaw)).Decode(&out); err != nil {
			b.Fatal(err)
		}
	}
}
