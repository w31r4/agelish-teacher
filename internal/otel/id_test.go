package otel

import "testing"

func TestDeterministicIDsAreStableAndSized(t *testing.T) {
	traceA := DeriveTraceID("session-1")
	traceB := DeriveTraceID("session-1")
	traceC := DeriveTraceID("session-2")

	if traceA != traceB {
		t.Fatalf("trace id must be stable: %q != %q", traceA, traceB)
	}
	if traceA == traceC {
		t.Fatalf("different inputs must produce different trace ids: %q", traceA)
	}
	if len(traceA) != 32 {
		t.Fatalf("trace id must be 16 bytes hex, got %d chars: %q", len(traceA), traceA)
	}

	spanA := DeriveSpanID("trace-request-1")
	spanB := DeriveSpanID("trace-request-1")
	if spanA != spanB {
		t.Fatalf("span id must be stable: %q != %q", spanA, spanB)
	}
	if len(spanA) != 16 {
		t.Fatalf("span id must be 8 bytes hex, got %d chars: %q", len(spanA), spanA)
	}
}
