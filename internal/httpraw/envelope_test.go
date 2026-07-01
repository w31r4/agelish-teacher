package httpraw

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEnvelopeBodyBytesSupportsTextBodyAndBase64(t *testing.T) {
	textEnvelope := Envelope{Body: "data: {\"ok\":true}\n\n"}
	textBody, err := textEnvelope.BodyBytes()
	if err != nil {
		t.Fatalf("decode text body: %v", err)
	}
	if string(textBody) != "data: {\"ok\":true}\n\n" {
		t.Fatalf("text body mismatch: %q", textBody)
	}

	base64Envelope := Envelope{BodyBase64: base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 255})}
	base64Body, err := base64Envelope.BodyBytes()
	if err != nil {
		t.Fatalf("decode base64 body: %v", err)
	}
	if string(base64Body) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("base64 body mismatch: %#v", base64Body)
	}
}

func TestDecodeJSONLReadsCanonicalRawHTTPEnvelopes(t *testing.T) {
	raw := strings.NewReader(`
{"source":"reasonix","request_id":"call_1","session_id":"sess_1","turn_id":"turn_1","direction":"request","method":"POST","url":"https://api.example.test/v1/chat/completions","headers":{"content-type":"application/json"},"body":"{\"model\":\"deepseek\"}","timestamp_ms":1710000000200}
{"source":"reasonix","request_id":"call_1","session_id":"sess_1","turn_id":"turn_1","direction":"response","status_code":200,"headers":{"content-type":"application/json"},"body_text":"{\"choices\":[]}","timestamp_ms":1710000001200}
`)

	envelopes, err := DecodeJSONL(raw)
	if err != nil {
		t.Fatalf("decode jsonl: %v", err)
	}
	if len(envelopes) != 2 {
		t.Fatalf("expected 2 envelopes, got %d", len(envelopes))
	}
	if envelopes[0].Direction != "request" || envelopes[1].Direction != "response" {
		t.Fatalf("directions mismatch: %#v", envelopes)
	}
	if envelopes[0].Headers["content-type"] != "application/json" {
		t.Fatalf("headers mismatch: %#v", envelopes[0].Headers)
	}
	body, err := envelopes[1].BodyBytes()
	if err != nil {
		t.Fatalf("decode body_text: %v", err)
	}
	if string(body) != `{"choices":[]}` {
		t.Fatalf("body_text mismatch: %q", body)
	}
}
