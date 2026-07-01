package httpraw

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/zenfun/agelish-teacher/internal/jsonx"
)

type Envelope struct {
	Source       string            `json:"source,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	RequestID    string            `json:"request_id,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	TurnID       string            `json:"turn_id,omitempty"`
	Direction    string            `json:"direction,omitempty"`
	Kind         string            `json:"kind,omitempty"`
	Method       string            `json:"method,omitempty"`
	URL          string            `json:"url,omitempty"`
	StatusCode   *int64            `json:"status_code,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyEncoding string            `json:"body_encoding,omitempty"`
	Body         string            `json:"body,omitempty"`
	BodyText     string            `json:"body_text,omitempty"`
	BodyBase64   string            `json:"body_base64,omitempty"`
	TimestampMS  int64             `json:"timestamp_ms,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

func (e Envelope) NormalizedDirection() string {
	direction := strings.ToLower(strings.TrimSpace(e.Direction))
	if direction == "" {
		direction = strings.ToLower(strings.TrimSpace(e.Kind))
	}
	return direction
}

func (e Envelope) BodyBytes() ([]byte, error) {
	if e.BodyBase64 != "" {
		body, err := base64.StdEncoding.DecodeString(e.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode body_base64: %w", err)
		}
		return body, nil
	}
	if e.BodyText != "" {
		return []byte(e.BodyText), nil
	}
	if e.Body != "" {
		if strings.EqualFold(strings.TrimSpace(e.BodyEncoding), "base64") {
			body, err := base64.StdEncoding.DecodeString(e.Body)
			if err != nil {
				return nil, fmt.Errorf("decode body as base64: %w", err)
			}
			return body, nil
		}
		return []byte(e.Body), nil
	}
	return nil, nil
}

func DecodeJSONL(r io.Reader) ([]Envelope, error) {
	reader := bufio.NewReader(r)
	var envelopes []Envelope
	lineNumber := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if strings.TrimSpace(line) != "" {
			lineNumber++
			var envelope Envelope
			if jsonErr := jsonx.Unmarshal([]byte(line), &envelope); jsonErr != nil {
				return nil, fmt.Errorf("decode envelope line %d: %w", lineNumber, jsonErr)
			}
			envelopes = append(envelopes, envelope)
		}
		if err == io.EOF {
			break
		}
	}
	return envelopes, nil
}
