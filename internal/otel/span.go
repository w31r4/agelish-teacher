package otel

type Span struct {
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	StartUnixNano int64          `json:"start_unix_nano"`
	EndUnixNano   int64          `json:"end_unix_nano"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Status        Status         `json:"status,omitempty"`
}

type Status struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
