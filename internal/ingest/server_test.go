package ingest_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zenfun/agelish-teacher/internal/ingest"
	"github.com/zenfun/agelish-teacher/internal/langfuse"
)

func TestPairHandlerForwardsOTLPToLangfuse(t *testing.T) {
	var posted atomic.Int64
	var postedBody string
	langfuseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/otel/v1/traces" {
			t.Fatalf("unexpected Langfuse path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-langfuse-ingestion-version"); got != "4" {
			t.Fatalf("missing ingestion version header: %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode OTLP payload: %v", err)
		}
		raw, _ := json.Marshal(payload)
		postedBody = string(raw)
		posted.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer langfuseServer.Close()

	server := ingest.NewServer(ingest.Config{
		Langfuse: langfuse.Client{
			BaseURL:   langfuseServer.URL,
			PublicKey: "pk-test",
			SecretKey: "sk-test",
		},
		Workers:        1,
		MaxPairBytes:   1 << 20,
		RequestTimeout: time.Second,
	})
	app := httptest.NewServer(server.Handler())
	defer app.Close()

	resp, err := http.Post(app.URL+"/v1/pairs", "application/json", strings.NewReader(reasonixPairJSON()))
	if err != nil {
		t.Fatalf("post pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status mismatch: %d", resp.StatusCode)
	}
	var got ingest.PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "sent" || got.TraceID == "" || got.SessionID != "sess_ingest" || got.SpanCount != 3 {
		t.Fatalf("unexpected pair response: %#v", got)
	}
	if posted.Load() != 1 {
		t.Fatalf("expected one Langfuse POST, got %d", posted.Load())
	}
	if strings.Contains(postedBody, "Bearer should-not-leak") {
		t.Fatalf("authorization header leaked into OTLP payload: %s", postedBody)
	}
	if !strings.Contains(postedBody, "Reasonix Chat Completions") {
		t.Fatalf("OTLP payload should contain generation name, got %s", postedBody)
	}

	statsResp, err := http.Get(app.URL + "/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer statsResp.Body.Close()
	var stats ingest.StatsSnapshot
	if err := json.NewDecoder(statsResp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.AcceptedTotal != 1 || stats.SentTotal != 1 || stats.FailedTotal != 0 || stats.InFlight != 0 {
		t.Fatalf("success stats mismatch: %#v", stats)
	}
}

func TestPairHandlerReportsLangfuseFailureAndStats(t *testing.T) {
	langfuseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down"))
	}))
	defer langfuseServer.Close()

	server := ingest.NewServer(ingest.Config{
		Langfuse: langfuse.Client{
			BaseURL:   langfuseServer.URL,
			PublicKey: "pk-test",
			SecretKey: "sk-test",
		},
		Workers:        1,
		MaxPairBytes:   1 << 20,
		RequestTimeout: time.Second,
	})
	app := httptest.NewServer(server.Handler())
	defer app.Close()

	resp, err := http.Post(app.URL+"/v1/pairs", "application/json", strings.NewReader(reasonixPairJSON()))
	if err != nil {
		t.Fatalf("post pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status mismatch: got %d want %d", resp.StatusCode, http.StatusBadGateway)
	}

	statsResp, err := http.Get(app.URL + "/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer statsResp.Body.Close()
	var stats ingest.StatsSnapshot
	if err := json.NewDecoder(statsResp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.AcceptedTotal != 1 || stats.SentTotal != 0 || stats.FailedTotal != 1 || stats.InFlight != 0 {
		t.Fatalf("stats mismatch: %#v", stats)
	}
	if !strings.Contains(stats.LastError, "503") {
		t.Fatalf("stats should include recent failure, got %#v", stats)
	}
}

func TestHealthEndpoint(t *testing.T) {
	server := ingest.NewServer(ingest.Config{Workers: 1, RequestTimeout: time.Second})
	app := httptest.NewServer(server.Handler())
	defer app.Close()

	resp, err := http.Get(app.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status mismatch: %d", resp.StatusCode)
	}
}

func reasonixPairJSON() string {
	return `{
		"source": "reasonix",
		"session_id": "sess_ingest",
		"turn_id": "turn_ingest_1",
		"request_id": "call_ingest_1",
		"request": {
			"method": "POST",
			"url": "https://api.example.test/v1/chat/completions",
			"headers": {
				"content-type": "application/json",
				"authorization": "Bearer should-not-leak"
			},
			"body_text": "{\"model\":\"deepseek-v4-pro\",\"messages\":[{\"role\":\"user\",\"content\":\"Say ingest ok.\"}]}",
			"timestamp_ms": 1710000000200
		},
		"response": {
			"status_code": 200,
			"headers": {"content-type": "application/json"},
			"body_text": "{\"model\":\"deepseek-v4-pro\",\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"ingest ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}",
			"timestamp_ms": 1710000001200
		}
	}`
}
