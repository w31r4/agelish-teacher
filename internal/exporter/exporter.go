package exporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/zenfun/agelish-teacher/internal/otel"
	"github.com/zenfun/agelish-teacher/internal/provider"
	_ "modernc.org/sqlite"
)

type Span = otel.Span

type Options struct {
	DBPath        string
	SessionID     string
	IncludeActive bool
}

type Result struct {
	Spans []otel.Span `json:"spans"`
}

type RawPairOptions struct {
	Provider     string
	Source       string
	SessionID    string
	RequestID    string
	RequestBody  []byte
	ResponseBody []byte
	StartedAt    time.Time
	EndedAt      time.Time
}

type sessionRow struct {
	ID        string
	Source    string
	Name      sql.NullString
	StartedAt epochMS
	EndedAt   nullableEpochMS
	Metadata  string
}

type turnRow struct {
	ID          string
	SessionID   string
	Number      int64
	Status      string
	StartedAt   epochMS
	EndedAt     nullableEpochMS
	AbortReason sql.NullString
}

type traceRequestRow struct {
	ID                  string
	SessionID           string
	TurnID              string
	ParentRequestID     sql.NullString
	RequestID           string
	Direction           string
	Provider            string
	Model               string
	RequestedModel      sql.NullString
	Timestamp           epochMS
	Summary             string
	Outcome             string
	ErrorType           sql.NullString
	ErrorMessage        sql.NullString
	HTTPStatus          sql.NullInt64
	StopReason          sql.NullString
	InputTokens         sql.NullInt64
	OutputTokens        sql.NullInt64
	ToolCallCount       int64
	CacheReadTokens     sql.NullInt64
	CacheCreationTokens sql.NullInt64
	ReasoningTokens     sql.NullInt64
	MaxTokens           sql.NullInt64
	DurationMS          sql.NullInt64
}

func Export(ctx context.Context, opts Options) (Result, error) {
	dbPath, err := resolveDBPath(opts.DBPath)
	if err != nil {
		return Result{}, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()

	sessions, err := loadSessions(ctx, db, opts)
	if err != nil {
		return Result{}, err
	}

	var result Result
	for _, session := range sessions {
		sessionSpans, err := exportSession(ctx, db, session)
		if err != nil {
			return Result{}, err
		}
		result.Spans = append(result.Spans, sessionSpans...)
	}
	return result, nil
}

func ExportRawPair(opts RawPairOptions) (Result, error) {
	providerName := strings.TrimSpace(opts.Provider)
	if providerName == "" {
		return Result{}, fmt.Errorf("raw provider is required")
	}
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = providerName
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID = "raw_" + providerName
	}
	requestID := strings.TrimSpace(opts.RequestID)
	if requestID == "" {
		requestID = "raw_request"
	}
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	endedAt := opts.EndedAt
	if endedAt.IsZero() || endedAt.Before(startedAt) {
		endedAt = startedAt
	}
	startMS := startedAt.UnixMilli()
	endMS := endedAt.UnixMilli()

	parsedRequest, _ := provider.ParseRequest(providerName, opts.RequestBody)
	parsedResponse, _ := provider.ParseResponse(providerName, opts.ResponseBody)
	toolResultsByID := map[string]provider.ToolResult{}
	for _, result := range parsedRequest.ToolResults {
		if result.ID != "" {
			toolResultsByID[result.ID] = result
		}
	}

	traceID := otel.DeriveTraceID("raw-session:" + sessionID)
	rootSpanID := otel.DeriveSpanID("raw-session:" + sessionID)
	turnSpanID := otel.DeriveSpanID("raw-turn:" + sessionID + ":1")
	rawSession := sessionRow{ID: sessionID, Source: source}
	rootName := sessionSpanName(rawSession)
	rootAttrs := map[string]any{
		"scribe.session.id":         sessionID,
		"scribe.source":             source,
		"gen_ai.conversation.id":    sessionID,
		"session.id":                sessionID,
		"langfuse.session.id":       sessionID,
		"langfuse.observation.type": "span",
		"langfuse.trace.name":       rootName,
		"agelish.input.kind":        "raw_http_body",
	}
	turnAttrs := map[string]any{
		"scribe.turn.id":            "raw_turn_1",
		"scribe.turn.number":        int64(1),
		"scribe.turn.status":        "completed",
		"session.id":                sessionID,
		"langfuse.session.id":       sessionID,
		"gen_ai.conversation.id":    sessionID,
		"langfuse.observation.type": "span",
		"langfuse.trace.name":       rootName,
		"agelish.input.kind":        "raw_http_body",
	}
	if len(parsedRequest.InputMessages) > 0 {
		rootAttrs["langfuse.trace.input"] = parsedRequest.InputMessages
		rootAttrs["langfuse.observation.input"] = parsedRequest.InputMessages
		turnAttrs["langfuse.observation.input"] = parsedRequest.InputMessages
	}
	if len(parsedResponse.OutputMessages) > 0 {
		rootAttrs["langfuse.trace.output"] = parsedResponse.OutputMessages
		rootAttrs["langfuse.observation.output"] = parsedResponse.OutputMessages
		turnAttrs["langfuse.observation.output"] = parsedResponse.OutputMessages
	}

	spans := []otel.Span{
		{
			TraceID:       traceID,
			SpanID:        rootSpanID,
			Name:          rootName,
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(startMS),
			EndUnixNano:   msToNs(endMS),
			Attributes:    rootAttrs,
		},
		{
			TraceID:       traceID,
			SpanID:        turnSpanID,
			ParentSpanID:  rootSpanID,
			Name:          turnSpanName(source, 1),
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(startMS),
			EndUnixNano:   msToNs(endMS),
			Attributes:    turnAttrs,
		},
	}

	attrs := map[string]any{
		"gen_ai.provider.name":       providerName,
		"gen_ai.operation.name":      "chat",
		"gen_ai.conversation.id":     sessionID,
		"session.id":                 sessionID,
		"langfuse.session.id":        sessionID,
		"langfuse.observation.type":  "generation",
		"langfuse.observation.level": "DEFAULT",
		"langfuse.trace.name":        rootName,
		"scribe.trace_request.id":    requestID,
		"scribe.request_id":          requestID,
		"agelish.input.kind":         "raw_http_body",
	}
	addInternalContextAttributes(attrs, parsedRequest.InternalContexts)
	if parsedRequest.Model != "" {
		attrs["gen_ai.request.model"] = parsedRequest.Model
	}
	if parsedResponse.Model != "" {
		attrs["gen_ai.response.model"] = parsedResponse.Model
	}
	setIntAttr(attrs, "gen_ai.usage.input_tokens", sql.NullInt64{}, parsedResponse.Usage.InputTokens)
	setIntAttr(attrs, "gen_ai.usage.output_tokens", sql.NullInt64{}, parsedResponse.Usage.OutputTokens)
	setIntAttr(attrs, "gen_ai.usage.cache_read.input_tokens", sql.NullInt64{}, parsedResponse.Usage.CacheReadTokens)
	setIntAttr(attrs, "gen_ai.usage.cache_creation.input_tokens", sql.NullInt64{}, parsedResponse.Usage.CacheCreationTokens)
	setIntAttr(attrs, "gen_ai.usage.reasoning.output_tokens", sql.NullInt64{}, parsedResponse.Usage.ReasoningTokens)
	setIntAttr(attrs, "gen_ai.request.max_tokens", sql.NullInt64{}, parsedRequest.MaxTokens)
	if len(parsedResponse.FinishReasons) > 0 {
		attrs["gen_ai.response.finish_reasons"] = parsedResponse.FinishReasons
	}
	if len(parsedRequest.SystemInstructions) > 0 {
		attrs["gen_ai.system_instructions"] = mustJSON(parsedRequest.SystemInstructions)
	}
	if len(parsedRequest.InputMessages) > 0 {
		attrs["gen_ai.input.messages"] = mustJSON(parsedRequest.InputMessages)
		attrs["gen_ai.prompt"] = mustJSON(parsedRequest.InputMessages)
		attrs["langfuse.observation.input"] = parsedRequest.InputMessages
	}
	if len(parsedResponse.OutputMessages) > 0 {
		attrs["gen_ai.output.messages"] = mustJSON(parsedResponse.OutputMessages)
		attrs["gen_ai.completion"] = mustJSON(parsedResponse.OutputMessages)
		attrs["langfuse.observation.output"] = parsedResponse.OutputMessages
	}

	nameModel := parsedResponse.Model
	if nameModel == "" {
		nameModel = parsedRequest.Model
	}
	generationSpanID := otel.DeriveSpanID("raw-generation:" + sessionID + ":" + requestID)
	spans = append(spans, otel.Span{
		TraceID:       traceID,
		SpanID:        generationSpanID,
		ParentSpanID:  turnSpanID,
		Name:          generationObservationName(providerName, nameModel, ""),
		Kind:          "SPAN_KIND_CLIENT",
		StartUnixNano: msToNs(startMS),
		EndUnixNano:   msToNs(endMS),
		Attributes:    attrs,
		Status:        otel.Status{Code: "STATUS_CODE_OK"},
	})

	for index, call := range parsedResponse.ToolCalls {
		toolID := call.ID
		if toolID == "" {
			toolID = fmt.Sprintf("%s:%d", requestID, index)
		}
		toolName := toolObservationName(call.Name)
		toolAttrs := map[string]any{
			"gen_ai.operation.name":     "execute_tool",
			"gen_ai.tool.name":          call.Name,
			"gen_ai.tool.call.id":       toolID,
			"session.id":                sessionID,
			"langfuse.session.id":       sessionID,
			"langfuse.observation.type": "tool",
			"langfuse.trace.name":       rootName,
			"scribe.trace_request.id":   requestID,
			"agelish.input.kind":        "raw_http_body",
		}
		if call.Name != "" {
			toolAttrs["scribe.tool.name"] = call.Name
		}
		if call.Namespace != "" {
			toolAttrs["gen_ai.tool.namespace"] = call.Namespace
		}
		if call.Arguments != nil {
			toolAttrs["gen_ai.tool.call.arguments"] = mustJSON(call.Arguments)
			toolAttrs["langfuse.observation.input"] = call.Arguments
		}
		if result, ok := toolResultsByID[call.ID]; ok {
			toolAttrs["scribe.tool.result.status"] = "matched"
			toolAttrs["gen_ai.tool.call.result"] = result.Output
			toolAttrs["langfuse.observation.output"] = result.Output
		} else {
			toolAttrs["scribe.tool.result.status"] = "missing"
			toolAttrs["langfuse.observation.output"] = map[string]any{
				"status": "missing_tool_result",
				"reason": "no matching tool result was provided in the raw request body",
			}
		}
		spans = append(spans, otel.Span{
			TraceID:       traceID,
			SpanID:        otel.DeriveSpanID("raw-tool:" + sessionID + ":" + requestID + ":" + toolID),
			ParentSpanID:  generationSpanID,
			Name:          toolName,
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(startMS),
			EndUnixNano:   msToNs(endMS),
			Attributes:    toolAttrs,
			Status:        otel.Status{Code: "STATUS_CODE_OK"},
		})
	}

	return Result{Spans: spans}, nil
}

func resolveDBPath(dbPath string) (string, error) {
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dbPath = filepath.Join(home, ".scribe", "traces.db")
	}
	if strings.HasPrefix(dbPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dbPath = filepath.Join(home, strings.TrimPrefix(dbPath, "~/"))
	}
	return dbPath, nil
}

func loadSessions(ctx context.Context, db *sql.DB, opts Options) ([]sessionRow, error) {
	query := `SELECT id, source, name, started_at, ended_at, metadata FROM sessions`
	var args []any
	var clauses []string
	if opts.SessionID != "" {
		clauses = append(clauses, "id = ?")
		args = append(args, opts.SessionID)
	}
	if !opts.IncludeActive {
		clauses = append(clauses, "ended_at IS NOT NULL")
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY started_at, id"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []sessionRow
	for rows.Next() {
		var row sessionRow
		if err := rows.Scan(&row.ID, &row.Source, &row.Name, &row.StartedAt, &row.EndedAt, &row.Metadata); err != nil {
			return nil, err
		}
		sessions = append(sessions, row)
	}
	return sessions, rows.Err()
}

func exportSession(ctx context.Context, db *sql.DB, session sessionRow) ([]otel.Span, error) {
	turns, err := loadTurns(ctx, db, session.ID)
	if err != nil {
		return nil, err
	}

	traceID := otel.DeriveTraceID("session:" + session.ID)
	rootSpanID := otel.DeriveSpanID("session:" + session.ID)
	rootEnd := session.StartedAt.Int64()
	if session.EndedAt.Valid {
		rootEnd = session.EndedAt.Int64
	}
	rootAttrs := map[string]any{
		"scribe.session.id":         session.ID,
		"scribe.source":             session.Source,
		"gen_ai.conversation.id":    session.ID,
		"session.id":                session.ID,
		"langfuse.session.id":       session.ID,
		"langfuse.observation.type": "span",
	}
	rootName := sessionSpanName(session)
	rootAttrs["langfuse.trace.name"] = rootName
	spans := []otel.Span{{
		TraceID:       traceID,
		SpanID:        rootSpanID,
		Name:          rootName,
		Kind:          "SPAN_KIND_INTERNAL",
		StartUnixNano: msToNs(session.StartedAt.Int64()),
		EndUnixNano:   msToNs(rootEnd),
		Attributes:    rootAttrs,
	}}

	var sessionInput any
	var sessionOutput any
	for _, turn := range turns {
		turnSpanID := otel.DeriveSpanID("turn:" + turn.ID)
		turnEnd := turn.StartedAt.Int64()
		if turn.EndedAt.Valid {
			turnEnd = turn.EndedAt.Int64
		}
		turnRequests, err := loadTraceRequests(ctx, db, turn.ID)
		if err != nil {
			return nil, err
		}
		turnPayloads, err := analyzeTurnPayloads(ctx, db, turnRequests)
		if err != nil {
			return nil, err
		}
		if sessionInput == nil && turnPayloads.Input != nil {
			sessionInput = turnPayloads.Input
		}
		if turnPayloads.Output != nil {
			sessionOutput = turnPayloads.Output
		}
		turnAttrs := map[string]any{
			"scribe.turn.id":            turn.ID,
			"scribe.turn.number":        turn.Number,
			"scribe.turn.status":        turn.Status,
			"session.id":                session.ID,
			"langfuse.session.id":       session.ID,
			"gen_ai.conversation.id":    session.ID,
			"langfuse.observation.type": "span",
			"langfuse.trace.name":       rootName,
		}
		if turnPayloads.Input != nil {
			turnAttrs["langfuse.observation.input"] = turnPayloads.Input
		}
		if turnPayloads.Output != nil {
			turnAttrs["langfuse.observation.output"] = turnPayloads.Output
		}
		spans = append(spans, otel.Span{
			TraceID:       traceID,
			SpanID:        turnSpanID,
			ParentSpanID:  rootSpanID,
			Name:          turnSpanName(session.Source, turn.Number),
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(turn.StartedAt.Int64()),
			EndUnixNano:   msToNs(turnEnd),
			Attributes:    turnAttrs,
		})

		requestByRequestID := map[string]traceRequestRow{}
		for _, tr := range turnRequests {
			if tr.Direction == "request" {
				requestByRequestID[tr.RequestID] = tr
			}
		}
		for _, tr := range turnRequests {
			if tr.Direction != "response" {
				continue
			}
			paired := requestByRequestID[tr.RequestID]
			observationSpans, err := buildObservationSpans(ctx, db, traceID, turnSpanID, session.ID, rootName, tr, paired, turnPayloads.ToolResultsByID)
			if err != nil {
				return nil, err
			}
			spans = append(spans, observationSpans...)
		}
	}
	if sessionInput != nil {
		rootAttrs["langfuse.trace.input"] = sessionInput
		rootAttrs["langfuse.observation.input"] = sessionInput
	}
	if sessionOutput != nil {
		rootAttrs["langfuse.trace.output"] = sessionOutput
		rootAttrs["langfuse.observation.output"] = sessionOutput
	}
	return spans, nil
}

func sessionSpanName(session sessionRow) string {
	label := shortSessionLabel(session.ID)
	if session.Name.Valid {
		name := strings.TrimSpace(session.Name.String)
		if name != "" && name != session.ID {
			label = name
		}
	}
	return fmt.Sprintf("%s - Session %s", sourceDisplayName(session.Source), label)
}

func turnSpanName(source string, number int64) string {
	return fmt.Sprintf("%s - Turn %d", sourceDisplayName(source), number)
}

func sourceDisplayName(source string) string {
	switch source {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	case "gemini-cli":
		return "Gemini CLI"
	default:
		if strings.TrimSpace(source) == "" {
			return "Scribe"
		}
		return source
	}
}

func shortSessionLabel(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func loadTurns(ctx context.Context, db *sql.DB, sessionID string) ([]turnRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, turn_number, status, started_at, ended_at, abort_reason FROM turns WHERE session_id = ? ORDER BY turn_number, started_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []turnRow
	for rows.Next() {
		var row turnRow
		if err := rows.Scan(&row.ID, &row.SessionID, &row.Number, &row.Status, &row.StartedAt, &row.EndedAt, &row.AbortReason); err != nil {
			return nil, err
		}
		turns = append(turns, row)
	}
	return turns, rows.Err()
}

func loadTraceRequests(ctx context.Context, db *sql.DB, turnID string) ([]traceRequestRow, error) {
	columns, err := tableColumns(ctx, db, "trace_requests")
	if err != nil {
		return nil, err
	}
	selectColumns := []string{
		traceColumn(columns, "id", "''"),
		traceColumn(columns, "session_id", "''"),
		traceColumn(columns, "turn_id", "''"),
		traceColumn(columns, "parent_request_id", "NULL"),
		traceColumn(columns, "request_id", "''"),
		traceColumn(columns, "direction", "''"),
		traceColumn(columns, "provider", "'unknown'"),
		traceColumn(columns, "model", "'unknown'"),
		traceColumn(columns, "requested_model", "NULL"),
		traceColumn(columns, "timestamp", "0"),
		traceColumn(columns, "summary", "'{}'"),
		traceColumn(columns, "outcome", "'ok'"),
		traceColumn(columns, "error_type", "NULL"),
		traceColumn(columns, "error_message", "NULL"),
		traceColumn(columns, "http_status", "NULL"),
		traceColumn(columns, "stop_reason", "NULL"),
		traceColumn(columns, "input_tokens", "NULL"),
		traceColumn(columns, "output_tokens", "NULL"),
		traceColumn(columns, "tool_call_count", "0"),
		traceColumn(columns, "cache_read_tokens", "NULL"),
		traceColumn(columns, "cache_creation_tokens", "NULL"),
		traceColumn(columns, "reasoning_tokens", "NULL"),
		traceColumn(columns, "max_tokens", "NULL"),
		traceColumn(columns, "duration_ms", "NULL"),
	}
	query := fmt.Sprintf(
		`SELECT %s FROM trace_requests WHERE turn_id = ? ORDER BY timestamp, id`,
		strings.Join(selectColumns, ", "),
	)
	rows, err := db.QueryContext(ctx, query, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []traceRequestRow
	for rows.Next() {
		var row traceRequestRow
		if err := rows.Scan(
			&row.ID, &row.SessionID, &row.TurnID, &row.ParentRequestID, &row.RequestID, &row.Direction,
			&row.Provider, &row.Model, &row.RequestedModel, &row.Timestamp, &row.Summary, &row.Outcome,
			&row.ErrorType, &row.ErrorMessage, &row.HTTPStatus, &row.StopReason, &row.InputTokens,
			&row.OutputTokens, &row.ToolCallCount, &row.CacheReadTokens, &row.CacheCreationTokens,
			&row.ReasoningTokens, &row.MaxTokens, &row.DurationMS,
		); err != nil {
			return nil, err
		}
		requests = append(requests, row)
	}
	return requests, rows.Err()
}

type turnPayloads struct {
	Input           any
	Output          any
	ToolResultsByID map[string]provider.ToolResult
}

func analyzeTurnPayloads(ctx context.Context, db *sql.DB, requests []traceRequestRow) (turnPayloads, error) {
	payloads := turnPayloads{
		ToolResultsByID: map[string]provider.ToolResult{},
	}
	for _, tr := range requests {
		body, err := rawPayload(ctx, db, tr.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return turnPayloads{}, err
		}
		switch tr.Direction {
		case "request":
			parsed, _ := provider.ParseRequest(tr.Provider, body)
			if payloads.Input == nil && len(parsed.InputMessages) > 0 {
				payloads.Input = parsed.InputMessages
			}
			summaries := []map[string]any{parseSummary(tr.Summary)}
			if payloads.Input == nil && shouldCreateControlSpan(summaries) {
				if input := controlInputPayload(summaries); len(input) > 0 {
					payloads.Input = input
				}
			}
			addToolResults(payloads.ToolResultsByID, parsed.ToolResults)
		case "response":
			parsed, _ := provider.ParseResponse(tr.Provider, body)
			if len(parsed.OutputMessages) > 0 {
				payloads.Output = parsed.OutputMessages
			}
			summaries := []map[string]any{parseSummary(tr.Summary)}
			if payloads.Output == nil && shouldCreateControlSpan(summaries) {
				if output := controlOutputPayload(tr, summaries); len(output) > 0 {
					payloads.Output = output
				}
			}
			addToolResults(payloads.ToolResultsByID, parsed.ToolResults)
		}
	}
	return payloads, nil
}

func addToolResults(byID map[string]provider.ToolResult, results []provider.ToolResult) {
	for _, result := range results {
		if strings.TrimSpace(result.ID) == "" {
			continue
		}
		byID[result.ID] = result
	}
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var typ sql.NullString
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func traceColumn(columns map[string]bool, name string, fallback string) string {
	if columns[name] {
		return name
	}
	return fallback + " AS " + name
}

func buildObservationSpans(ctx context.Context, db *sql.DB, traceID string, turnSpanID string, sessionID string, traceName string, response traceRequestRow, request traceRequestRow, toolResultsByID map[string]provider.ToolResult) ([]otel.Span, error) {
	responseBody, err := rawPayload(ctx, db, response.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	requestBody, err := rawPayload(ctx, db, request.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	parsedRequest, _ := provider.ParseRequest(response.Provider, requestBody)
	parsedResponse, _ := provider.ParseResponse(response.Provider, responseBody)
	requestSummary := parseSummary(request.Summary)
	responseSummary := parseSummary(response.Summary)
	summaries := []map[string]any{responseSummary, requestSummary}

	startMS := response.Timestamp.Int64()
	if request.ID != "" {
		startMS = request.Timestamp.Int64()
	}
	endMS := response.Timestamp.Int64()
	if response.DurationMS.Valid && request.ID != "" {
		endMS = request.Timestamp.Int64() + response.DurationMS.Int64
	}

	parentSpanID := turnSpanID
	if response.ParentRequestID.Valid && response.ParentRequestID.String != "" {
		parentSpanID = otel.DeriveSpanID("trace_request:" + response.ParentRequestID.String)
	}
	spanID := otel.DeriveSpanID("trace_request:" + response.ID)
	role := summaryString(summaries, "request_role")
	fineRole := summaryString(summaries, "fine_role", "fine_request_role")
	if shouldCreateControlSpan(summaries) {
		attrs := map[string]any{
			"scribe.provider.name":       response.Provider,
			"scribe.trace_request.id":    response.ID,
			"scribe.request_id":          response.RequestID,
			"session.id":                 sessionID,
			"langfuse.session.id":        sessionID,
			"langfuse.observation.type":  "span",
			"langfuse.trace.name":        traceName,
			"langfuse.observation.level": langfuseLevel(response.Outcome),
		}
		addScribeSummaryAttributes(attrs, summaries)
		if input := controlInputPayload(summaries); len(input) > 0 {
			attrs["langfuse.observation.input"] = input
		}
		if output := controlOutputPayload(response, summaries); len(output) > 0 {
			attrs["langfuse.observation.output"] = output
		}
		status := responseStatus(response)
		return []otel.Span{{
			TraceID:       traceID,
			SpanID:        spanID,
			ParentSpanID:  parentSpanID,
			Name:          controlObservationName(summaries),
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(startMS),
			EndUnixNano:   msToNs(endMS),
			Attributes:    attrs,
			Status:        status,
		}}, nil
	}
	attrs := map[string]any{
		"gen_ai.provider.name":       response.Provider,
		"gen_ai.operation.name":      "chat",
		"gen_ai.conversation.id":     sessionID,
		"session.id":                 sessionID,
		"langfuse.session.id":        sessionID,
		"langfuse.observation.type":  "generation",
		"langfuse.trace.name":        traceName,
		"langfuse.observation.level": langfuseLevel(response.Outcome),
		"scribe.trace_request.id":    response.ID,
		"scribe.request_id":          response.RequestID,
	}
	addScribeSummaryAttributes(attrs, summaries)
	addInternalContextAttributes(attrs, parsedRequest.InternalContexts)
	if response.RequestedModel.Valid && response.RequestedModel.String != "" {
		attrs["gen_ai.request.model"] = response.RequestedModel.String
	} else if request.Model != "" {
		attrs["gen_ai.request.model"] = request.Model
	}
	if response.Model != "" && response.Model != "unknown" {
		attrs["gen_ai.response.model"] = response.Model
	} else if parsedResponse.Model != "" {
		attrs["gen_ai.response.model"] = parsedResponse.Model
	}
	setIntAttr(attrs, "gen_ai.usage.input_tokens", response.InputTokens, parsedResponse.Usage.InputTokens)
	setIntAttr(attrs, "gen_ai.usage.output_tokens", response.OutputTokens, parsedResponse.Usage.OutputTokens)
	setIntAttr(attrs, "gen_ai.usage.cache_read.input_tokens", response.CacheReadTokens, parsedResponse.Usage.CacheReadTokens)
	setIntAttr(attrs, "gen_ai.usage.cache_creation.input_tokens", response.CacheCreationTokens, parsedResponse.Usage.CacheCreationTokens)
	setIntAttr(attrs, "gen_ai.usage.reasoning.output_tokens", response.ReasoningTokens, parsedResponse.Usage.ReasoningTokens)
	setIntAttr(attrs, "gen_ai.request.max_tokens", response.MaxTokens, parsedRequest.MaxTokens)
	if response.ToolCallCount > 0 {
		attrs["scribe.tool_call_count"] = response.ToolCallCount
	}
	if response.HTTPStatus.Valid {
		attrs["http.response.status_code"] = response.HTTPStatus.Int64
	}
	if response.StopReason.Valid && response.StopReason.String != "" {
		attrs["gen_ai.response.finish_reasons"] = []string{response.StopReason.String}
	} else if len(parsedResponse.FinishReasons) > 0 {
		attrs["gen_ai.response.finish_reasons"] = parsedResponse.FinishReasons
	}
	if len(parsedRequest.SystemInstructions) > 0 {
		attrs["gen_ai.system_instructions"] = mustJSON(parsedRequest.SystemInstructions)
	}
	if len(parsedRequest.InputMessages) > 0 {
		attrs["gen_ai.input.messages"] = mustJSON(parsedRequest.InputMessages)
		attrs["gen_ai.prompt"] = mustJSON(parsedRequest.InputMessages)
		attrs["langfuse.observation.input"] = parsedRequest.InputMessages
	}
	if isErrorResponse(response) {
		errorType := generationErrorType(response)
		attrs["error.type"] = errorType
		if response.ErrorMessage.Valid && response.ErrorMessage.String != "" {
			attrs["langfuse.observation.status_message"] = response.ErrorMessage.String
		}
		attrs["langfuse.observation.output"] = errorObservationOutput(response, errorType)
	}
	if !isErrorResponse(response) && len(parsedResponse.OutputMessages) > 0 {
		attrs["gen_ai.output.messages"] = mustJSON(parsedResponse.OutputMessages)
		attrs["gen_ai.completion"] = mustJSON(parsedResponse.OutputMessages)
		attrs["langfuse.observation.output"] = parsedResponse.OutputMessages
	}

	status := responseStatus(response)
	nameModel := response.Model
	if nameModel == "" || nameModel == "unknown" {
		nameModel = parsedResponse.Model
	}
	name := generationObservationName(response.Provider, nameModel, role)

	var spans []otel.Span
	if shouldCreateAgentSpan(role, fineRole) {
		agentSpanID := otel.DeriveSpanID("agent:" + response.ID)
		agentAttrs := map[string]any{
			"gen_ai.operation.name":      "invoke_agent",
			"gen_ai.conversation.id":     sessionID,
			"session.id":                 sessionID,
			"langfuse.session.id":        sessionID,
			"langfuse.observation.type":  "agent",
			"langfuse.trace.name":        traceName,
			"langfuse.observation.level": langfuseLevel(response.Outcome),
			"scribe.trace_request.id":    response.ID,
			"scribe.request_id":          response.RequestID,
		}
		addScribeSummaryAttributes(agentAttrs, summaries)
		addInternalContextAttributes(agentAttrs, parsedRequest.InternalContexts)
		if len(parsedRequest.InputMessages) > 0 {
			agentAttrs["langfuse.observation.input"] = parsedRequest.InputMessages
		}
		if len(parsedResponse.OutputMessages) > 0 {
			agentAttrs["langfuse.observation.output"] = parsedResponse.OutputMessages
		}
		spans = append(spans, otel.Span{
			TraceID:       traceID,
			SpanID:        agentSpanID,
			ParentSpanID:  parentSpanID,
			Name:          agentObservationName(role, fineRole),
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(startMS),
			EndUnixNano:   msToNs(endMS),
			Attributes:    agentAttrs,
			Status:        status,
		})
		parentSpanID = agentSpanID
	}

	span := otel.Span{
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentSpanID,
		Name:          name,
		Kind:          "SPAN_KIND_CLIENT",
		StartUnixNano: msToNs(startMS),
		EndUnixNano:   msToNs(endMS),
		Attributes:    attrs,
		Status:        status,
	}
	spans = append(spans, span)

	for index, call := range parsedResponse.ToolCalls {
		toolID := call.ID
		if toolID == "" {
			toolID = fmt.Sprintf("%s:%d", response.ID, index)
		}
		toolName := toolObservationName(call.Name)
		toolAttrs := map[string]any{
			"gen_ai.operation.name":     "execute_tool",
			"gen_ai.tool.name":          call.Name,
			"gen_ai.tool.call.id":       toolID,
			"session.id":                sessionID,
			"langfuse.session.id":       sessionID,
			"langfuse.observation.type": "tool",
			"langfuse.trace.name":       traceName,
			"scribe.trace_request.id":   response.ID,
		}
		if call.Name != "" {
			toolAttrs["scribe.tool.name"] = call.Name
		}
		addScribeSummaryAttributes(toolAttrs, summaries)
		if call.Namespace != "" {
			toolAttrs["gen_ai.tool.namespace"] = call.Namespace
		}
		if call.Arguments != nil {
			toolAttrs["gen_ai.tool.call.arguments"] = mustJSON(call.Arguments)
			toolAttrs["langfuse.observation.input"] = call.Arguments
		}
		if result, ok := toolResultsByID[call.ID]; ok {
			toolAttrs["scribe.tool.result.status"] = "matched"
			toolAttrs["gen_ai.tool.call.result"] = result.Output
			toolAttrs["langfuse.observation.output"] = result.Output
		} else {
			toolAttrs["scribe.tool.result.status"] = "missing"
			toolAttrs["langfuse.observation.output"] = map[string]any{
				"status": "missing_tool_result",
				"reason": "no matching tool result was captured in this Scribe turn",
			}
		}
		spans = append(spans, otel.Span{
			TraceID:       traceID,
			SpanID:        otel.DeriveSpanID("tool:" + response.ID + ":" + toolID),
			ParentSpanID:  spanID,
			Name:          toolName,
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: span.StartUnixNano,
			EndUnixNano:   span.EndUnixNano,
			Attributes:    toolAttrs,
			Status:        otel.Status{Code: "STATUS_CODE_OK"},
		})
	}
	return spans, nil
}

func generationObservationName(provider string, model string, role string) string {
	name := strings.TrimSpace(provider + " " + model)
	if strings.EqualFold(provider, "codex") {
		name = strings.TrimSpace("Codex " + model)
	}
	if name == "" {
		name = "gen_ai.client"
	}
	if role != "" && role != "primary" {
		name = strings.TrimSpace(roleDisplayName(role) + " " + name)
	}
	return name
}

func toolObservationName(name string) string {
	switch name {
	case "exec_command", "shell", "local_shell", "local_shell_call":
		return "Shell command"
	case "apply_patch":
		return "Apply patch"
	case "write_stdin":
		return "Write stdin"
	case "read_mcp_resource":
		return "Read MCP resource"
	case "list_mcp_resources":
		return "List MCP resources"
	case "list_mcp_resource_templates":
		return "List MCP resource templates"
	default:
		if strings.TrimSpace(name) == "" {
			return "Tool"
		}
		return name
	}
}

func roleDisplayName(role string) string {
	switch role {
	case "subagent":
		return "Subagent"
	case "assistive":
		return "Assistive"
	case "auxiliary":
		return "Auxiliary"
	case "probe":
		return "Probe"
	default:
		return role
	}
}

func parseSummary(raw string) map[string]any {
	var summary map[string]any
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		return map[string]any{}
	}
	return summary
}

func summaryString(summaries []map[string]any, keys ...string) string {
	for _, summary := range summaries {
		for _, key := range keys {
			if value, ok := summary[key]; ok {
				switch got := value.(type) {
				case string:
					if strings.TrimSpace(got) != "" {
						return got
					}
				case nil:
					continue
				default:
					text := strings.TrimSpace(fmt.Sprint(got))
					if text != "" {
						return text
					}
				}
			}
		}
	}
	return ""
}

func summaryBool(summaries []map[string]any, key string) (bool, bool) {
	for _, summary := range summaries {
		if value, ok := summary[key]; ok {
			if got, ok := value.(bool); ok {
				return got, true
			}
		}
	}
	return false, false
}

func summaryInt64(summaries []map[string]any, keys ...string) (int64, bool) {
	for _, summary := range summaries {
		for _, key := range keys {
			if value, ok := summary[key]; ok {
				if got := asSummaryInt64(value); got != nil {
					return *got, true
				}
			}
		}
	}
	return 0, false
}

func asSummaryInt64(value any) *int64 {
	switch got := value.(type) {
	case float64:
		v := int64(got)
		return &v
	case int:
		v := int64(got)
		return &v
	case int64:
		return &got
	case json.Number:
		if v, err := got.Int64(); err == nil {
			return &v
		}
	}
	return nil
}

func addScribeSummaryAttributes(attrs map[string]any, summaries []map[string]any) {
	stringAttrs := map[string][]string{
		"scribe.request_role":                       {"request_role"},
		"scribe.fine_role":                          {"fine_role", "fine_request_role"},
		"scribe.request_role.classifier":            {"request_role_classifier"},
		"scribe.request_role.classifier_confidence": {"request_role_classifier_confidence"},
		"scribe.request_role.classifier_source":     {"request_role_classifier_source"},
		"scribe.request_role.source":                {"request_role_source"},
		"scribe.agent.type":                         {"agent_type"},
		"scribe.agent.subagent_kind":                {"subagent_kind"},
		"scribe.phase":                              {"phase"},
		"scribe.path":                               {"path"},
		"scribe.codex.thread_id":                    {"codex_thread_id"},
		"scribe.codex.session_id":                   {"codex_session_id"},
		"scribe.codex.turn_id":                      {"codex_turn_id"},
		"scribe.codex.request_kind":                 {"codex_request_kind"},
		"scribe.codex.thread_source":                {"codex_thread_source"},
		"scribe.codex.sandbox":                      {"codex_sandbox"},
		"scribe.control_kind":                       {"control_kind"},
	}
	for attr, keys := range stringAttrs {
		if value := summaryString(summaries, keys...); value != "" {
			attrs[attr] = value
		}
	}
	if isStream, ok := summaryBool(summaries, "is_stream"); ok {
		attrs["scribe.is_stream"] = isStream
	}
	if modelCount, ok := summaryInt64(summaries, "model_count"); ok {
		attrs["scribe.model_count"] = modelCount
	}
}

func addInternalContextAttributes(attrs map[string]any, contexts []provider.InternalContext) {
	if len(contexts) == 0 {
		return
	}
	var sources []string
	seenSources := map[string]bool{}
	for _, context := range contexts {
		if context.Source != "" && !seenSources[context.Source] {
			sources = append(sources, context.Source)
			seenSources[context.Source] = true
		}
		if context.Source == "goal" {
			attrs["scribe.codex.goal.present"] = true
			if context.Objective != "" {
				attrs["scribe.codex.goal.objective"] = context.Objective
			}
		}
	}
	if len(sources) > 0 {
		attrs["scribe.codex.internal_context.sources"] = sources
	}
}

func shouldCreateControlSpan(summaries []map[string]any) bool {
	if summaryString(summaries, "control_kind") != "" {
		return true
	}
	if summaryString(summaries, "phase") == "control" || summaryString(summaries, "agent_type") == "control" {
		return true
	}
	if summaryString(summaries, "request_role") == "probe" {
		return true
	}
	return strings.HasPrefix(summaryString(summaries, "path"), "/models")
}

func controlInputPayload(summaries []map[string]any) map[string]any {
	input := map[string]any{}
	for _, key := range []string{"path", "control_kind", "request_role", "phase"} {
		if value := summaryString(summaries, key); value != "" {
			input[key] = value
		}
	}
	return input
}

func controlOutputPayload(response traceRequestRow, summaries []map[string]any) map[string]any {
	output := map[string]any{}
	if response.HTTPStatus.Valid {
		output["http_status"] = response.HTTPStatus.Int64
	}
	if modelCount, ok := summaryInt64(summaries, "model_count"); ok {
		output["model_count"] = modelCount
	}
	if value := summaryString(summaries, "control_kind"); value != "" {
		output["control_kind"] = value
	}
	return output
}

func controlObservationName(summaries []map[string]any) string {
	if controlKind := summaryString(summaries, "control_kind"); controlKind != "" {
		return humanizeIdentifier(controlKind)
	}
	if path := summaryString(summaries, "path"); path != "" {
		return "Codex control " + path
	}
	return "Codex control"
}

func shouldCreateAgentSpan(role string, fineRole string) bool {
	switch role {
	case "subagent", "assistive", "auxiliary":
		return true
	}
	return strings.Contains(fineRole, "subagent")
}

func responseStatus(response traceRequestRow) otel.Status {
	status := otel.Status{}
	if isErrorResponse(response) {
		status.Code = "STATUS_CODE_ERROR"
		if response.ErrorMessage.Valid {
			status.Message = response.ErrorMessage.String
		}
	} else {
		status.Code = "STATUS_CODE_OK"
	}
	return status
}

func isErrorResponse(response traceRequestRow) bool {
	return response.Outcome == "error" || (response.HTTPStatus.Valid && response.HTTPStatus.Int64 >= 400)
}

func generationErrorType(response traceRequestRow) string {
	if response.ErrorType.Valid {
		if normalized := normalizeErrorType(response.ErrorType.String); normalized != "" {
			return normalized
		}
	}
	if response.HTTPStatus.Valid && response.HTTPStatus.Int64 >= 400 {
		switch response.HTTPStatus.Int64 {
		case 502:
			return "http_502"
		case 503:
			return "http_503"
		case 504:
			return "http_504"
		default:
			if response.HTTPStatus.Int64 >= 500 {
				return "http_5xx"
			}
			return "http_4xx"
		}
	}
	message := strings.ToLower(response.ErrorMessage.String)
	switch {
	case strings.Contains(message, "connection reset"):
		return "connection_reset"
	case strings.Contains(message, "cannot connect"), strings.Contains(message, "connect to host"):
		return "connect_error"
	case strings.Contains(message, "timeout"), strings.Contains(message, "timed out"), strings.Contains(message, "deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "upstream"), strings.Contains(message, "bad gateway"):
		return "upstream_error"
	default:
		return "error"
	}
}

func normalizeErrorType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isWord {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func errorObservationOutput(response traceRequestRow, errorType string) map[string]any {
	output := map[string]any{
		"status":     "error",
		"error_type": errorType,
	}
	if response.ErrorMessage.Valid && response.ErrorMessage.String != "" {
		output["message"] = response.ErrorMessage.String
	} else if response.HTTPStatus.Valid {
		output["message"] = fmt.Sprintf("HTTP %d", response.HTTPStatus.Int64)
	}
	if response.HTTPStatus.Valid {
		output["http_status"] = response.HTTPStatus.Int64
	}
	return output
}

func agentObservationName(role string, fineRole string) string {
	switch {
	case role != "" && fineRole != "":
		return roleDisplayName(role) + " " + humanizeIdentifier(fineRole)
	case role != "":
		return roleDisplayName(role)
	case fineRole != "":
		return humanizeIdentifier(fineRole)
	default:
		return "agent"
	}
}

func humanizeIdentifier(value string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(value))
	for i, word := range words {
		if strings.EqualFold(word, "codex") {
			words[i] = "Codex"
			continue
		}
		if i == 0 && word != "" {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func rawPayload(ctx context.Context, db *sql.DB, traceRequestID string) ([]byte, error) {
	if traceRequestID == "" {
		return nil, sql.ErrNoRows
	}
	var body []byte
	var encoding string
	if err := db.QueryRowContext(ctx, `SELECT body, content_encoding FROM raw_payloads WHERE trace_request_id = ?`, traceRequestID).Scan(&body, &encoding); err != nil {
		return nil, err
	}
	if encoding == "zstd" {
		reader, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return reader.DecodeAll(body, nil)
	}
	return body, nil
}

func setIntAttr(attrs map[string]any, key string, structured sql.NullInt64, parsed *int64) {
	if structured.Valid {
		attrs[key] = structured.Int64
		return
	}
	if parsed != nil {
		attrs[key] = *parsed
	}
}

func msToNs(ms int64) int64 {
	return ms * 1_000_000
}

type epochMS int64

func (e *epochMS) Scan(value any) error {
	parsed, err := parseEpochMS(value)
	if err != nil {
		return err
	}
	*e = epochMS(parsed)
	return nil
}

func (e epochMS) Int64() int64 {
	return int64(e)
}

type nullableEpochMS struct {
	Int64 int64
	Valid bool
}

func (n *nullableEpochMS) Scan(value any) error {
	if value == nil {
		n.Int64 = 0
		n.Valid = false
		return nil
	}
	parsed, err := parseEpochMS(value)
	if err != nil {
		return err
	}
	n.Int64 = parsed
	n.Valid = true
	return nil
}

func parseEpochMS(value any) (int64, error) {
	switch got := value.(type) {
	case int64:
		return got, nil
	case int:
		return int64(got), nil
	case float64:
		return int64(got), nil
	case []byte:
		return parseEpochMSString(string(got))
	case string:
		return parseEpochMSString(got)
	case time.Time:
		return got.UnixMilli(), nil
	default:
		return 0, fmt.Errorf("unsupported timestamp type %T", value)
	}
}

func parseEpochMSString(value string) (int64, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, nil
	}
	if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
		return parsed, nil
	}
	timestamp, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, err
	}
	return timestamp.UnixMilli(), nil
}

func langfuseLevel(outcome string) string {
	switch outcome {
	case "warning":
		return "WARNING"
	case "error":
		return "ERROR"
	default:
		return "DEFAULT"
	}
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}
