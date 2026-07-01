package langfuse

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientPostsOTLPJSONToLangfuseEndpointWithRequiredHeaders(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotVersion string
	var gotContentType string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("x-langfuse-ingestion-version")
		gotContentType = r.Header.Get("Content-Type")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := Client{
		BaseURL:   server.URL,
		PublicKey: "pk_test",
		SecretKey: "sk_test",
	}
	err := client.PostOTLPJSON(context.Background(), []byte(`{"resourceSpans":[]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotPath != "/api/public/otel/v1/traces" {
		t.Fatalf("path mismatch: %q", gotPath)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("pk_test:sk_test"))
	if gotAuth != wantAuth {
		t.Fatalf("auth mismatch: got %q want %q", gotAuth, wantAuth)
	}
	if gotVersion != "4" {
		t.Fatalf("ingestion version mismatch: %q", gotVersion)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("content type mismatch: %q", gotContentType)
	}
	if gotBody != `{"resourceSpans":[]}` {
		t.Fatalf("body mismatch: %q", gotBody)
	}
}
