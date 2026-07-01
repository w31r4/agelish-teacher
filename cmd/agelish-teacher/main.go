package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/zenfun/agelish-teacher/internal/exporter"
	"github.com/zenfun/agelish-teacher/internal/langfuse"
	"github.com/zenfun/agelish-teacher/internal/otlp"
	"github.com/zenfun/agelish-teacher/internal/semconv"
)

func main() {
	var dbPath string
	var sessionID string
	var includeActive bool
	var outPath string
	var format string
	var send bool
	var checkStandard bool
	var langfuseURL string
	var publicKey string
	var secretKey string

	flag.StringVar(&dbPath, "db", "", "Scribe SQLite DB path; defaults to ~/.scribe/traces.db")
	flag.StringVar(&sessionID, "session", "", "export only one Scribe session id")
	flag.BoolVar(&includeActive, "include-active", false, "include sessions without ended_at")
	flag.StringVar(&outPath, "out", "", "write output JSON to this path instead of stdout")
	flag.StringVar(&format, "format", "otlp-json", "output format: otlp-json or spans")
	flag.BoolVar(&send, "send", false, "POST OTLP JSON to Langfuse")
	flag.BoolVar(&checkStandard, "check-standard", false, "validate generated spans against Agelish Teacher's GenAI OTel profile")
	flag.StringVar(&langfuseURL, "langfuse-url", os.Getenv("LANGFUSE_BASE_URL"), "Langfuse base URL")
	flag.StringVar(&publicKey, "langfuse-public-key", os.Getenv("LANGFUSE_PUBLIC_KEY"), "Langfuse public key")
	flag.StringVar(&secretKey, "langfuse-secret-key", os.Getenv("LANGFUSE_SECRET_KEY"), "Langfuse secret key")
	flag.Parse()

	ctx := context.Background()
	result, err := exporter.Export(ctx, exporter.Options{
		DBPath:        dbPath,
		SessionID:     sessionID,
		IncludeActive: includeActive,
	})
	if err != nil {
		fatal(err)
	}
	if checkStandard {
		findings := semconv.ValidateSpans(result.Spans)
		if len(findings) > 0 {
			rawFindings, _ := json.MarshalIndent(findings, "", "  ")
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

	raw, err := json.MarshalIndent(payload, "", "  ")
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agelish-teacher:", err)
	os.Exit(1)
}
