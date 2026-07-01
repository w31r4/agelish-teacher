package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/zenfun/agelish-teacher/internal/exporter"
	"github.com/zenfun/agelish-teacher/internal/httpraw"
	"github.com/zenfun/agelish-teacher/internal/ingest"
	"github.com/zenfun/agelish-teacher/internal/jsonx"
	"github.com/zenfun/agelish-teacher/internal/langfuse"
	"github.com/zenfun/agelish-teacher/internal/otlp"
	"github.com/zenfun/agelish-teacher/internal/semconv"
)

func main() {
	var exportCfg exportConfig
	var outPath string
	var format string
	var send bool
	var checkStandard bool
	var langfuseURL string
	var publicKey string
	var secretKey string
	var serve bool
	var listen string
	var workers int
	var maxPairBytes int64
	var requestTimeout time.Duration

	flag.StringVar(&exportCfg.DBPath, "db", "", "Scribe SQLite DB path; defaults to ~/.scribe/traces.db")
	flag.StringVar(&exportCfg.SessionID, "session", "", "export only one Scribe session id")
	flag.BoolVar(&exportCfg.IncludeActive, "include-active", false, "include sessions without ended_at")
	flag.StringVar(&outPath, "out", "", "write output JSON to this path instead of stdout")
	flag.StringVar(&format, "format", "otlp-json", "output format: otlp-json or spans")
	flag.BoolVar(&send, "send", false, "POST OTLP JSON to Langfuse")
	flag.BoolVar(&checkStandard, "check-standard", false, "validate generated spans against Agelish Teacher's GenAI OTel profile")
	flag.StringVar(&langfuseURL, "langfuse-url", os.Getenv("LANGFUSE_BASE_URL"), "Langfuse base URL")
	flag.StringVar(&publicKey, "langfuse-public-key", os.Getenv("LANGFUSE_PUBLIC_KEY"), "Langfuse public key")
	flag.StringVar(&secretKey, "langfuse-secret-key", os.Getenv("LANGFUSE_SECRET_KEY"), "Langfuse secret key")
	flag.StringVar(&exportCfg.RawProvider, "raw-provider", "", "provider for raw HTTP body conversion, e.g. codex, anthropic, openai")
	flag.StringVar(&exportCfg.RawSource, "raw-source", "", "source label for raw HTTP body conversion; defaults to raw-provider")
	flag.StringVar(&exportCfg.RawRequestPath, "raw-request", "", "path to a raw HTTP request body JSON/SSE file")
	flag.StringVar(&exportCfg.RawResponsePath, "raw-response", "", "path to a raw HTTP response body JSON/SSE file")
	flag.StringVar(&exportCfg.RawSessionID, "raw-session-id", "", "session id for raw HTTP body conversion")
	flag.StringVar(&exportCfg.RawRequestID, "raw-request-id", "", "request id for raw HTTP body conversion")
	flag.StringVar(&exportCfg.RawEnvelopePath, "raw-envelope", "", "path to canonical raw HTTP envelope JSONL")
	flag.BoolVar(&exportCfg.RawEnvelopeStdin, "raw-envelope-stdin", false, "read canonical raw HTTP envelope JSONL from stdin")
	flag.BoolVar(&serve, "serve", false, "run local HTTP raw pair ingest server")
	flag.StringVar(&listen, "listen", "127.0.0.1:4319", "address for -serve to listen on")
	flag.IntVar(&workers, "workers", 0, "maximum concurrent pair ingests for -serve; defaults to runtime.NumCPU")
	flag.Int64Var(&maxPairBytes, "max-pair-bytes", 64<<20, "maximum JSON request size for -serve /v1/pairs")
	flag.DurationVar(&requestTimeout, "request-timeout", 30*time.Second, "per-request timeout for -serve")
	flag.Parse()

	ctx := context.Background()
	if serve {
		client := langfuse.Client{
			BaseURL:   langfuseURL,
			PublicKey: publicKey,
			SecretKey: secretKey,
		}
		ingestCfg := ingest.Config{
			Langfuse:       client,
			Workers:        workers,
			MaxPairBytes:   maxPairBytes,
			RequestTimeout: requestTimeout,
		}
		if err := serveIngest(listen, ingestCfg); err != nil {
			fatal(err)
		}
		return
	}

	result, err := exportResult(ctx, exportCfg)
	if err != nil {
		fatal(err)
	}
	if checkStandard {
		findings := semconv.ValidateSpans(result.Spans)
		if len(findings) > 0 {
			rawFindings, _ := jsonx.MarshalIndent(findings, "", "  ")
			_, _ = os.Stderr.Write(rawFindings)
			_, _ = os.Stderr.Write([]byte("\n"))
			os.Exit(2)
		}
	}

	var payload any
	switch format {
	case "otlp-json":
		payload = otlp.BuildTracePayload(result.Spans)
	case "spans":
		payload = result
	default:
		fatal(fmt.Errorf("unknown format %q", format))
	}

	raw, err := jsonx.MarshalIndent(payload, "", "  ")
	if err != nil {
		fatal(err)
	}
	raw = append(raw, '\n')

	if outPath != "" {
		if err := os.WriteFile(outPath, raw, 0o644); err != nil {
			fatal(err)
		}
	} else {
		if _, err := os.Stdout.Write(raw); err != nil {
			fatal(err)
		}
	}

	if send {
		if format != "otlp-json" {
			fatal(fmt.Errorf("-send requires -format otlp-json"))
		}
		if err := (langfuse.Client{
			BaseURL:   langfuseURL,
			PublicKey: publicKey,
			SecretKey: secretKey,
		}).PostOTLPJSON(ctx, raw); err != nil {
			fatal(err)
		}
	}
}

func serveIngest(listen string, cfg ingest.Config) error {
	server := ingest.NewServer(cfg)
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "agelish-teacher: serving local ingest on http://%s\n", listen)
	err := httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agelish-teacher:", err)
	os.Exit(1)
}

type exportConfig struct {
	DBPath           string
	SessionID        string
	IncludeActive    bool
	RawProvider      string
	RawSource        string
	RawRequestPath   string
	RawResponsePath  string
	RawSessionID     string
	RawRequestID     string
	RawEnvelopePath  string
	RawEnvelopeStdin bool
}

func exportResult(ctx context.Context, cfg exportConfig) (exporter.Result, error) {
	if cfg.RawEnvelopePath != "" || cfg.RawEnvelopeStdin {
		envelopes, err := readRawEnvelopes(cfg.RawEnvelopePath, cfg.RawEnvelopeStdin)
		if err != nil {
			return exporter.Result{}, err
		}
		return exporter.ExportRawEnvelopes(exporter.RawEnvelopeOptions{Envelopes: envelopes})
	}
	if cfg.RawProvider != "" || cfg.RawRequestPath != "" || cfg.RawResponsePath != "" {
		requestBody, err := readOptionalFile(cfg.RawRequestPath)
		if err != nil {
			return exporter.Result{}, fmt.Errorf("read raw request: %w", err)
		}
		responseBody, err := readOptionalFile(cfg.RawResponsePath)
		if err != nil {
			return exporter.Result{}, fmt.Errorf("read raw response: %w", err)
		}
		return exporter.ExportRawPair(exporter.RawPairOptions{
			Provider:     cfg.RawProvider,
			Source:       cfg.RawSource,
			SessionID:    cfg.RawSessionID,
			RequestID:    cfg.RawRequestID,
			RequestBody:  requestBody,
			ResponseBody: responseBody,
		})
	}
	return exporter.Export(ctx, exporter.Options{
		DBPath:        cfg.DBPath,
		SessionID:     cfg.SessionID,
		IncludeActive: cfg.IncludeActive,
	})
}

func readRawEnvelopes(path string, stdin bool) ([]httpraw.Envelope, error) {
	if path != "" && stdin {
		return nil, fmt.Errorf("-raw-envelope and -raw-envelope-stdin are mutually exclusive")
	}
	if stdin {
		return httpraw.DecodeJSONL(os.Stdin)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open raw envelope: %w", err)
	}
	defer file.Close()
	return httpraw.DecodeJSONL(file)
}

func readOptionalFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	return os.ReadFile(path)
}
