package ingest

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zenfun/agelish-teacher/internal/exporter"
	"github.com/zenfun/agelish-teacher/internal/httpraw"
	"github.com/zenfun/agelish-teacher/internal/jsonx"
	"github.com/zenfun/agelish-teacher/internal/otlp"
)

type Forwarder interface {
	PostOTLPJSON(ctx context.Context, payload []byte) error
}

type Config struct {
	Langfuse       Forwarder
	Workers        int
	MaxPairBytes   int64
	RequestTimeout time.Duration
}

type Server struct {
	langfuse       Forwarder
	sem            chan struct{}
	maxPairBytes   int64
	requestTimeout time.Duration
	stats          stats
}

type stats struct {
	acceptedTotal atomic.Int64
	sentTotal     atomic.Int64
	failedTotal   atomic.Int64
	inFlight      atomic.Int64
	lastError     atomic.Value
}

type StatsSnapshot struct {
	AcceptedTotal int64  `json:"accepted_total"`
	SentTotal     int64  `json:"sent_total"`
	FailedTotal   int64  `json:"failed_total"`
	InFlight      int64  `json:"in_flight"`
	LastError     string `json:"last_error,omitempty"`
}

type PairRequest struct {
	Source    string           `json:"source,omitempty"`
	Provider  string           `json:"provider,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
	TurnID    string           `json:"turn_id,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
	Metadata  map[string]any   `json:"metadata,omitempty"`
	Request   httpraw.Envelope `json:"request"`
	Response  httpraw.Envelope `json:"response"`
}

type PairResponse struct {
	Status    string `json:"status"`
	TraceID   string `json:"trace_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	SpanCount int    `json:"span_count,omitempty"`
}

func NewServer(cfg Config) *Server {
	workers := cfg.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
		if workers <= 0 {
			workers = 1
		}
	}
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Server{
		langfuse:       cfg.Langfuse,
		sem:            make(chan struct{}, workers),
		maxPairBytes:   cfg.MaxPairBytes,
		requestTimeout: timeout,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/v1/pairs", s.handlePair)
	return mux
}

func (s *Server) Snapshot() StatsSnapshot {
	snapshot := StatsSnapshot{
		AcceptedTotal: s.stats.acceptedTotal.Load(),
		SentTotal:     s.stats.sentTotal.Load(),
		FailedTotal:   s.stats.failedTotal.Load(),
		InFlight:      s.stats.inFlight.Load(),
	}
	if value := s.stats.lastError.Load(); value != nil {
		if text, ok := value.(string); ok {
			snapshot.LastError = text
		}
	}
	return snapshot
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.Snapshot())
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	var cancel context.CancelFunc
	if s.requestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.requestTimeout)
		defer cancel()
	}
	if !s.acquire(ctx) {
		http.Error(w, "ingest timeout", http.StatusServiceUnavailable)
		return
	}
	defer s.release()

	body := r.Body
	if s.maxPairBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.maxPairBytes)
	}
	defer body.Close()

	var pair PairRequest
	if err := jsonx.NewDecoder(body).Decode(&pair); err != nil {
		s.recordFailure(err)
		http.Error(w, fmt.Sprintf("decode pair: %v", err), http.StatusBadRequest)
		return
	}
	result, err := exporter.ExportRawEnvelopes(exporter.RawEnvelopeOptions{
		Envelopes: pair.Envelopes(),
	})
	if err != nil {
		s.recordFailure(err)
		http.Error(w, fmt.Sprintf("export pair: %v", err), http.StatusBadRequest)
		return
	}
	payload, err := jsonx.Marshal(otlp.BuildTracePayload(result.Spans))
	if err != nil {
		s.recordFailure(err)
		http.Error(w, fmt.Sprintf("encode otlp: %v", err), http.StatusInternalServerError)
		return
	}
	if s.langfuse == nil {
		err := fmt.Errorf("langfuse forwarder is required")
		s.recordFailure(err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.langfuse.PostOTLPJSON(ctx, payload); err != nil {
		s.recordFailure(err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.stats.acceptedTotal.Add(1)
	s.stats.sentTotal.Add(1)
	response := PairResponse{
		Status:    "sent",
		SessionID: strings.TrimSpace(pair.SessionID),
		SpanCount: len(result.Spans),
	}
	if len(result.Spans) > 0 {
		response.TraceID = result.Spans[0].TraceID
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) acquire(ctx context.Context) bool {
	select {
	case s.sem <- struct{}{}:
		s.stats.inFlight.Add(1)
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) release() {
	<-s.sem
	s.stats.inFlight.Add(-1)
}

func (s *Server) recordFailure(err error) {
	s.stats.acceptedTotal.Add(1)
	s.stats.failedTotal.Add(1)
	s.stats.lastError.Store(err.Error())
}

func (p PairRequest) Envelopes() []httpraw.Envelope {
	request := p.withCommonFields(p.Request, "request")
	response := p.withCommonFields(p.Response, "response")
	return []httpraw.Envelope{request, response}
}

func (p PairRequest) withCommonFields(envelope httpraw.Envelope, direction string) httpraw.Envelope {
	envelope.Source = strings.TrimSpace(p.Source)
	envelope.Provider = strings.TrimSpace(p.Provider)
	envelope.SessionID = strings.TrimSpace(p.SessionID)
	envelope.TurnID = strings.TrimSpace(p.TurnID)
	envelope.RequestID = strings.TrimSpace(p.RequestID)
	envelope.Direction = direction
	envelope.Headers = RedactHeaders(envelope.Headers)
	if len(envelope.Metadata) == 0 {
		envelope.Metadata = p.Metadata
	}
	return envelope
}

func RedactHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	redacted := make(map[string]string, len(headers))
	for key, value := range headers {
		if isSensitiveHeader(key) {
			redacted[key] = "[REDACTED]"
			continue
		}
		redacted[key] = value
	}
	return redacted
}

func isSensitiveHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "cookie", "set-cookie", "x-api-key", "proxy-authorization":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = jsonx.NewEncoder(w).Encode(value)
}
