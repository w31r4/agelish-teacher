package langfuse

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	BaseURL    string
	PublicKey  string
	SecretKey  string
	HTTPClient *http.Client
}

func (c Client) PostOTLPJSON(ctx context.Context, payload []byte) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		return fmt.Errorf("langfuse base URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/public/otel/v1/traces", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-langfuse-ingestion-version", "4")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.PublicKey+":"+c.SecretKey)))

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("langfuse OTLP POST failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}
