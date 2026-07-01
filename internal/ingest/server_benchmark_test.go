package ingest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zenfun/agelish-teacher/internal/ingest"
)

type benchmarkForwarder struct{}

func (benchmarkForwarder) PostOTLPJSON(context.Context, []byte) error {
	return nil
}

func BenchmarkPairHandlerEndToEnd(b *testing.B) {
	server := ingest.NewServer(ingest.Config{
		Langfuse:       benchmarkForwarder{},
		Workers:        8,
		MaxPairBytes:   1 << 20,
		RequestTimeout: time.Second,
	})
	handler := server.Handler()
	body := reasonixPairJSON()

	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/v1/pairs", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status mismatch: %d body=%s", rec.Code, rec.Body.String())
		}
	}
}
