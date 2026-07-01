package jsonx

import (
	"bytes"
	"strings"
	"testing"
)

type samplePayload struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

func TestMarshalUnmarshalAndStreamingHelpers(t *testing.T) {
	input := samplePayload{Source: "reasonix", Count: 2}
	raw, err := Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded samplePayload
	if err := Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded != input {
		t.Fatalf("decoded mismatch: %#v", decoded)
	}

	var viaReader samplePayload
	if err := NewDecoder(strings.NewReader(string(raw))).Decode(&viaReader); err != nil {
		t.Fatalf("stream decode: %v", err)
	}
	if viaReader != input {
		t.Fatalf("stream decoded mismatch: %#v", viaReader)
	}

	var out bytes.Buffer
	if err := NewEncoder(&out).Encode(input); err != nil {
		t.Fatalf("stream encode: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"source":"reasonix"`)) {
		t.Fatalf("encoded output mismatch: %s", out.String())
	}
}
