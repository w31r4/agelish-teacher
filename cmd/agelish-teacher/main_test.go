package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportResultReadsRawEnvelopeJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envelopes.jsonl")
	raw := strings.Join([]string{
		`{"source":"reasonix","request_id":"call_cli","session_id":"sess_cli","turn_id":"turn_cli","direction":"request","method":"POST","url":"https://api.example.test/v1/chat/completions","body":"{\"model\":\"deepseek-v4-pro\",\"messages\":[{\"role\":\"user\",\"content\":\"Say cli ok.\"}]}","timestamp_ms":1710000000200}`,
		`{"source":"reasonix","request_id":"call_cli","session_id":"sess_cli","turn_id":"turn_cli","direction":"response","status_code":200,"body":"{\"model\":\"deepseek-v4-pro\",\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"cli ok\"},\"finish_reason\":\"stop\"}]}","timestamp_ms":1710000001200}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write envelope fixture: %v", err)
	}

	result, err := exportResult(context.Background(), exportConfig{RawEnvelopePath: path})
	if err != nil {
		t.Fatalf("export result: %v", err)
	}

	var generationName string
	for _, span := range result.Spans {
		if span.Attributes["langfuse.observation.type"] == "generation" {
			generationName = span.Name
			if span.Attributes["gen_ai.provider.name"] != "reasonix" {
				t.Fatalf("provider mismatch: %#v", span.Attributes)
			}
			if span.Attributes["scribe.provider.name"] != "openai" {
				t.Fatalf("scribe provider mismatch: %#v", span.Attributes)
			}
		}
	}
	if generationName != "Reasonix Chat Completions" {
		t.Fatalf("generation name mismatch: %q", generationName)
	}
}
