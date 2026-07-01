package exporter

import (
	"context"
	"database/sql"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/zenfun/agelish-teacher/internal/httpraw"
	"github.com/zenfun/agelish-teacher/internal/jsonx"
	"github.com/zenfun/agelish-teacher/internal/otel"
	"github.com/zenfun/agelish-teacher/internal/provider"
	_ "modernc.org/sqlite"
)

type Span = otel.Span

var volatileObjectRefPattern = regexp.MustCompile(`\s+[A-Za-z_][A-Za-z0-9_]*:<[^>]*\sobject at 0x[0-9a-fA-F]+>`)

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

type RawEnvelopeOptions struct {
	Envelopes []httpraw.Envelope
	InputKind string
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
	return ExportRawEnvelopes(RawEnvelopeOptions{
		InputKind: "raw_http_body",
		Envelopes: []httpraw.Envelope{
			{
				Source:      source,
				Provider:    providerName,
				SessionID:   sessionID,
				TurnID:      "raw_turn_1",
				RequestID:   requestID,
				Direction:   "request",
				BodyBase64:  base64Body(opts.RequestBody),
				TimestampMS: startedAt.UnixMilli(),
			},
			{
				Source:      source,
				Provider:    providerName,
				SessionID:   sessionID,
				TurnID:      "raw_turn_1",
				RequestID:   requestID,
				Direction:   "response",
				StatusCode:  int64Ptr(200),
				BodyBase64:  base64Body(opts.ResponseBody),
				TimestampMS: endedAt.UnixMilli(),
			},
		},
	})
}

func ExportRawEnvelopes(opts RawEnvelopeOptions) (Result, error) {
	if len(opts.Envelopes) == 0 {
		return Result{}, fmt.Errorf("raw envelopes are required")
	}
	inputKind := strings.TrimSpace(opts.InputKind)
	if inputKind == "" {
		inputKind = "raw_http_envelope"
	}
	sessions, err := rawEnvelopeSessions(opts.Envelopes)
	if err != nil {
		return Result{}, err
	}
	var result Result
	for _, session := range sessions {
		spans, err := exportRawEnvelopeSession(session, inputKind)
		if err != nil {
			return Result{}, err
		}
		result.Spans = append(result.Spans, spans...)
	}
	return result, nil
}

type tracePayload struct {
	Row  traceRequestRow
	Body []byte
}

type rawEnvelopeSession struct {
	row   sessionRow
	turns []rawEnvelopeTurn
}

type rawEnvelopeTurn struct {
	row      turnRow
	payloads []tracePayload
}

func rawEnvelopeSessions(envelopes []httpraw.Envelope) ([]rawEnvelopeSession, error) {
	type mutableTurn struct {
		row      turnRow
		payloads []tracePayload
	}
	type mutableSession struct {
		row   sessionRow
		turns map[string]*mutableTurn
	}

	sessionsByID := map[string]*mutableSession{}
	pendingByTurn := map[string][]string{}
	providerByPair := map[string]string{}
	fallbackRequestSeq := 0
	for index, envelope := range envelopes {
		direction := envelope.NormalizedDirection()
		if direction != "request" && direction != "response" {
			return nil, fmt.Errorf("raw envelope %d has unsupported direction %q", index+1, direction)
		}
		body, err := envelope.BodyBytes()
		if err != nil {
			return nil, fmt.Errorf("raw envelope %d body: %w", index+1, err)
		}
		providerName := rawEnvelopeProvider(envelope)
		source := strings.TrimSpace(envelope.Source)
		if source == "" {
			source = providerName
		}
		if source == "" || source == "unknown" {
			source = "raw-http"
		}
		sessionID := strings.TrimSpace(envelope.SessionID)
		if sessionID == "" {
			sessionID = "raw_" + source
		}
		turnID := strings.TrimSpace(envelope.TurnID)
		if turnID == "" {
			turnID = "raw_turn_1"
		}
		pendingKey := sessionID + "\x00" + turnID
		requestID := strings.TrimSpace(envelope.RequestID)
		if requestID == "" {
			if direction == "request" {
				fallbackRequestSeq++
				requestID = fmt.Sprintf("raw_request_%d", fallbackRequestSeq)
				pendingByTurn[pendingKey] = append(pendingByTurn[pendingKey], requestID)
			} else if pending := pendingByTurn[pendingKey]; len(pending) > 0 {
				requestID = pending[0]
				pendingByTurn[pendingKey] = pending[1:]
			} else {
				fallbackRequestSeq++
				requestID = fmt.Sprintf("raw_request_%d", fallbackRequestSeq)
			}
		}
		pairKey := pendingKey + "\x00" + requestID
		if providerName == "unknown" && direction == "response" {
			if pairedProvider := providerByPair[pairKey]; pairedProvider != "" {
				providerName = pairedProvider
			}
		}
		if direction == "request" && providerName != "" && providerName != "unknown" {
			providerByPair[pairKey] = providerName
		}
		timestampMS := envelope.TimestampMS
		if timestampMS == 0 {
			timestampMS = int64(index + 1)
		}
		parsed, _ := parseEnvelopePayload(providerName, direction, body)
		model := parsed.Model
		if model == "" {
			model = "unknown"
		}
		summary := rawEnvelopeSummary(envelope, providerName, source)
		row := traceRequestRow{
			ID:        rawTraceRequestID(direction, sessionID, turnID, requestID),
			SessionID: sessionID,
			TurnID:    turnID,
			RequestID: requestID,
			Direction: direction,
			Provider:  providerName,
			Model:     model,
			Timestamp: epochMS(timestampMS),
			Summary:   jsonx.String(summary),
			Outcome:   "ok",
		}
		if direction == "response" {
			if envelope.StatusCode != nil {
				row.HTTPStatus = sql.NullInt64{Int64: *envelope.StatusCode, Valid: true}
				if *envelope.StatusCode >= 400 {
					row.Outcome = "error"
				}
			}
			if len(parsed.FinishReasons) > 0 {
				row.StopReason = sql.NullString{String: parsed.FinishReasons[0], Valid: true}
			}
			if parsedFinishReasonIsError(parsed) {
				row.Outcome = "error"
			}
		}

		session := sessionsByID[sessionID]
		if session == nil {
			session = &mutableSession{
				row: sessionRow{
					ID:        sessionID,
					Source:    source,
					StartedAt: epochMS(timestampMS),
					EndedAt:   nullableEpochMS{Int64: timestampMS, Valid: true},
					Metadata:  "{}",
				},
				turns: map[string]*mutableTurn{},
			}
			sessionsByID[sessionID] = session
		}
		if timestampMS < session.row.StartedAt.Int64() {
			session.row.StartedAt = epochMS(timestampMS)
		}
		if !session.row.EndedAt.Valid || timestampMS > session.row.EndedAt.Int64 {
			session.row.EndedAt = nullableEpochMS{Int64: timestampMS, Valid: true}
		}
		turn := session.turns[turnID]
		if turn == nil {
			turn = &mutableTurn{
				row: turnRow{
					ID:        turnID,
					SessionID: sessionID,
					Status:    "completed",
					StartedAt: epochMS(timestampMS),
					EndedAt:   nullableEpochMS{Int64: timestampMS, Valid: true},
				},
			}
			session.turns[turnID] = turn
		}
		if timestampMS < turn.row.StartedAt.Int64() {
			turn.row.StartedAt = epochMS(timestampMS)
		}
		if !turn.row.EndedAt.Valid || timestampMS > turn.row.EndedAt.Int64 {
			turn.row.EndedAt = nullableEpochMS{Int64: timestampMS, Valid: true}
		}
		turn.payloads = append(turn.payloads, tracePayload{Row: row, Body: body})
	}

	sessionIDs := make([]string, 0, len(sessionsByID))
	for id := range sessionsByID {
		sessionIDs = append(sessionIDs, id)
	}
	sort.Slice(sessionIDs, func(i, j int) bool {
		left := sessionsByID[sessionIDs[i]].row
		right := sessionsByID[sessionIDs[j]].row
		if left.StartedAt.Int64() != right.StartedAt.Int64() {
			return left.StartedAt.Int64() < right.StartedAt.Int64()
		}
		return left.ID < right.ID
	})

	sessions := make([]rawEnvelopeSession, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		mutable := sessionsByID[sessionID]
		turnIDs := make([]string, 0, len(mutable.turns))
		for id := range mutable.turns {
			turnIDs = append(turnIDs, id)
		}
		sort.Slice(turnIDs, func(i, j int) bool {
			left := mutable.turns[turnIDs[i]].row
			right := mutable.turns[turnIDs[j]].row
			if left.StartedAt.Int64() != right.StartedAt.Int64() {
				return left.StartedAt.Int64() < right.StartedAt.Int64()
			}
			return left.ID < right.ID
		})
		turns := make([]rawEnvelopeTurn, 0, len(turnIDs))
		for i, turnID := range turnIDs {
			mutableTurn := mutable.turns[turnID]
			mutableTurn.row.Number = int64(i + 1)
			sort.Slice(mutableTurn.payloads, func(i, j int) bool {
				left := mutableTurn.payloads[i].Row
				right := mutableTurn.payloads[j].Row
				if left.Timestamp.Int64() != right.Timestamp.Int64() {
					return left.Timestamp.Int64() < right.Timestamp.Int64()
				}
				return left.ID < right.ID
			})
			turns = append(turns, rawEnvelopeTurn{row: mutableTurn.row, payloads: mutableTurn.payloads})
		}
		sessions = append(sessions, rawEnvelopeSession{row: mutable.row, turns: turns})
	}
	return sessions, nil
}

func exportRawEnvelopeSession(session rawEnvelopeSession, inputKind string) ([]otel.Span, error) {
	traceID := otel.DeriveTraceID("raw-session:" + session.row.ID)
	rootSpanID := otel.DeriveSpanID("raw-session:" + session.row.ID)
	rootEnd := session.row.StartedAt.Int64()
	if session.row.EndedAt.Valid {
		rootEnd = session.row.EndedAt.Int64
	}
	rootName := sessionSpanName(session.row)
	rootAttrs := map[string]any{
		"scribe.session.id":         session.row.ID,
		"scribe.source":             session.row.Source,
		"gen_ai.conversation.id":    session.row.ID,
		"session.id":                session.row.ID,
		"langfuse.session.id":       session.row.ID,
		"langfuse.observation.type": "span",
		"langfuse.trace.name":       rootName,
		"agelish.input.kind":        inputKind,
	}
	spans := []otel.Span{{
		TraceID:       traceID,
		SpanID:        rootSpanID,
		Name:          rootName,
		Kind:          "SPAN_KIND_INTERNAL",
		StartUnixNano: msToNs(session.row.StartedAt.Int64()),
		EndUnixNano:   msToNs(rootEnd),
		Attributes:    rootAttrs,
	}}

	var sessionInput any
	var sessionOutput any
	generationIndex := 0
	for _, turn := range session.turns {
		turnSpanID := otel.DeriveSpanID("raw-turn:" + session.row.ID + ":" + turn.row.ID)
		turnEnd := turn.row.StartedAt.Int64()
		if turn.row.EndedAt.Valid {
			turnEnd = turn.row.EndedAt.Int64
		}
		turnPayloads := analyzeTracePayloads(turn.payloads)
		if sessionInput == nil && turnPayloads.Input != nil {
			sessionInput = turnPayloads.Input
		}
		if turnPayloads.Output != nil {
			sessionOutput = turnPayloads.Output
		}
		turnAttrs := map[string]any{
			"scribe.turn.id":            turn.row.ID,
			"scribe.turn.number":        turn.row.Number,
			"scribe.turn.status":        turn.row.Status,
			"session.id":                session.row.ID,
			"langfuse.session.id":       session.row.ID,
			"gen_ai.conversation.id":    session.row.ID,
			"langfuse.observation.type": "span",
			"langfuse.trace.name":       rootName,
			"agelish.input.kind":        inputKind,
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
			Name:          turnSpanName(session.row.Source, turn.row.Number),
			Kind:          "SPAN_KIND_INTERNAL",
			StartUnixNano: msToNs(turn.row.StartedAt.Int64()),
			EndUnixNano:   msToNs(turnEnd),
			Attributes:    turnAttrs,
		})

		requestByRequestID := map[string]tracePayload{}
		for _, payload := range turn.payloads {
			if payload.Row.Direction == "request" {
				requestByRequestID[payload.Row.RequestID] = payload
			}
		}
		for _, payload := range turn.payloads {
			if payload.Row.Direction != "response" {
				continue
			}
			paired := requestByRequestID[payload.Row.RequestID]
			observationSpans, err := buildObservationSpansFromPayloads(traceID, turnSpanID, session.row.ID, rootName, payload.Row, paired.Row, payload.Body, paired.Body, turnPayloads.ToolResultsByID, &generationIndex)
			if err != nil {
				return nil, err
			}
			addInputKind(observationSpans, inputKind)
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

func base64Body(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(body)
}

func int64Ptr(value int64) *int64 {
	return &value
}

func parseEnvelopePayload(providerName string, direction string, body []byte) (provider.ParsedPayload, error) {
	if direction == "request" {
		return provider.ParseRequest(providerName, body)
	}
	return provider.ParseResponse(providerName, body)
}

func rawEnvelopeProvider(envelope httpraw.Envelope) string {
	if providerName := strings.ToLower(strings.TrimSpace(envelope.Provider)); providerName != "" {
		return providerName
	}
	source := strings.ToLower(strings.TrimSpace(envelope.Source))
	switch source {
	case "anthropic", "codex", "openai", "openrouter":
		return source
	case "claude", "claude-code":
		return "anthropic"
	}
	path := rawEnvelopePath(envelope.URL)
	switch {
	case strings.Contains(path, "/chat/completions"), strings.Contains(path, "/responses"):
		return "openai"
	case strings.Contains(path, "/messages"):
		return "anthropic"
	default:
		return "unknown"
	}
}

func rawEnvelopeSummary(envelope httpraw.Envelope, providerName string, source string) map[string]any {
	summary := map[string]any{}
	if source != "" {
		summary["platform"] = source
	}
	path := rawEnvelopePath(envelope.URL)
	if path != "" {
		summary["path"] = path
	}
	if method := strings.TrimSpace(envelope.Method); method != "" {
		summary["http_method"] = strings.ToUpper(method)
	}
	if len(envelope.Headers) > 0 {
		if envelope.NormalizedDirection() == "response" {
			summary["response_headers"] = envelope.Headers
		} else {
			summary["request_headers"] = envelope.Headers
		}
	}
	if providerName == "openai" && strings.Contains(path, "/chat/completions") {
		summary["protocol"] = "openai.chat_completions"
		summary["operation_name"] = rawOperationDisplayName(source) + " Chat Completions"
	}
	for key, value := range envelope.Metadata {
		if _, exists := summary[key]; !exists {
			summary[key] = value
		}
	}
	return summary
}

func rawEnvelopePath(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Path != "" {
		return parsed.Path
	}
	return rawURL
}

func rawOperationDisplayName(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "Raw HTTP"
	}
	switch source {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "openai":
		return "OpenAI"
	case "openrouter":
		return "OpenRouter"
	}
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(source))
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	if len(words) == 0 {
		return source
	}
	return strings.Join(words, " ")
}

func rawTraceRequestID(direction string, sessionID string, turnID string, requestID string) string {
	return strings.Join([]string{"raw", direction, sessionID, turnID, requestID}, ":")
}

func addInputKind(spans []otel.Span, inputKind string) {
	for i := range spans {
		if spans[i].Attributes == nil {
			spans[i].Attributes = map[string]any{}
		}
		spans[i].Attributes["agelish.input.kind"] = inputKind
	}
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
	generationIndex := 0
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
			observationSpans, err := buildObservationSpans(ctx, db, traceID, turnSpanID, session.ID, rootName, tr, paired, turnPayloads.ToolResultsByID, &generationIndex)
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
	var tracePayloads []tracePayload
	for _, tr := range requests {
		body, err := rawPayload(ctx, db, tr.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return turnPayloads{}, err
		}
		tracePayloads = append(tracePayloads, tracePayload{Row: tr, Body: body})
	}
	return analyzeTracePayloads(tracePayloads), nil
}

func analyzeTracePayloads(tracePayloads []tracePayload) turnPayloads {
	payloads := turnPayloads{
		ToolResultsByID: map[string]provider.ToolResult{},
	}
	for _, payload := range tracePayloads {
		tr := payload.Row
		switch tr.Direction {
		case "request":
			parsed, _ := provider.ParseRequest(tr.Provider, payload.Body)
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
			parsed, _ := provider.ParseResponse(tr.Provider, payload.Body)
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
	return payloads
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

func buildObservationSpans(ctx context.Context, db *sql.DB, traceID string, turnSpanID string, sessionID string, traceName string, response traceRequestRow, request traceRequestRow, toolResultsByID map[string]provider.ToolResult, generationIndex *int) ([]otel.Span, error) {
	responseBody, err := rawPayload(ctx, db, response.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	requestBody, err := rawPayload(ctx, db, request.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	return buildObservationSpansFromPayloads(traceID, turnSpanID, sessionID, traceName, response, request, responseBody, requestBody, toolResultsByID, generationIndex)
}

func buildObservationSpansFromPayloads(traceID string, turnSpanID string, sessionID string, traceName string, response traceRequestRow, request traceRequestRow, responseBody []byte, requestBody []byte, toolResultsByID map[string]provider.ToolResult, generationIndex *int) ([]otel.Span, error) {
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
		status := responseStatus(response, provider.ParsedPayload{})
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
	includeSystemInDisplay := true
	if generationIndex != nil {
		includeSystemInDisplay = *generationIndex == 0
		*generationIndex = *generationIndex + 1
	}
	displayProvider := generationProviderName(response.Provider, summaries)
	attrs := map[string]any{
		"gen_ai.provider.name":       displayProvider,
		"gen_ai.operation.name":      "chat",
		"gen_ai.conversation.id":     sessionID,
		"session.id":                 sessionID,
		"langfuse.session.id":        sessionID,
		"langfuse.observation.type":  "generation",
		"langfuse.trace.name":        traceName,
		"langfuse.observation.level": langfuseLevel(response.Outcome),
		"scribe.trace_request.id":    response.ID,
		"scribe.request_id":          response.RequestID,
		"scribe.provider.name":       response.Provider,
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
	addSystemInstructionAttributes(attrs, parsedRequest)
	if len(parsedRequest.InputMessages) > 0 {
		attrs["gen_ai.input.messages"] = jsonx.String(parsedRequest.InputMessages)
		attrs["gen_ai.prompt"] = jsonx.String(parsedRequest.InputMessages)
		attrs["langfuse.observation.input"] = langfuseDisplayInput(parsedRequest.InputMessages, includeSystemInDisplay)
	}
	responseIsError := isGenerationErrorResponse(response, parsedResponse)
	if responseIsError {
		errorType := generationErrorType(response)
		attrs["error.type"] = errorType
		if message := generationErrorMessage(response, parsedResponse); message != "" {
			attrs["langfuse.observation.status_message"] = message
		}
		attrs["langfuse.observation.output"] = errorObservationOutput(response, errorType, parsedResponse)
	}
	if !responseIsError && len(parsedResponse.OutputMessages) > 0 {
		attrs["gen_ai.output.messages"] = jsonx.String(parsedResponse.OutputMessages)
		attrs["gen_ai.completion"] = jsonx.String(parsedResponse.OutputMessages)
		attrs["langfuse.observation.output"] = parsedResponse.OutputMessages
	}

	status := responseStatus(response, parsedResponse)
	nameModel := response.Model
	if nameModel == "" || nameModel == "unknown" {
		nameModel = parsedResponse.Model
	}
	name := generationDisplayName(displayProvider, nameModel, role, summaries)

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
		addSystemInstructionAttributes(agentAttrs, parsedRequest)
		if len(parsedRequest.InputMessages) > 0 {
			agentAttrs["langfuse.observation.input"] = langfuseDisplayInput(parsedRequest.InputMessages, includeSystemInDisplay)
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
			toolAttrs["gen_ai.tool.call.arguments"] = jsonx.String(call.Arguments)
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

func generationProviderName(fallback string, summaries []map[string]any) string {
	if platform := summaryString(summaries, "platform"); platform != "" {
		return platform
	}
	return fallback
}

func generationDisplayName(provider string, model string, role string, summaries []map[string]any) string {
	if operationName := summaryString(summaries, "operation_name"); operationName != "" {
		if role != "" && role != "primary" {
			return strings.TrimSpace(roleDisplayName(role) + " " + operationName)
		}
		return operationName
	}
	return generationObservationName(provider, model, role)
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
	if err := jsonx.Unmarshal([]byte(raw), &summary); err != nil {
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
	case stdjson.Number:
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
		"scribe.platform":                           {"platform"},
		"scribe.protocol":                           {"protocol"},
		"scribe.operation.name":                     {"operation_name"},
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

func addSystemInstructionAttributes(attrs map[string]any, parsed provider.ParsedPayload) {
	instructions := systemInstructionTexts(parsed)
	if len(instructions) == 0 {
		return
	}
	encoded := jsonx.String(instructions)
	attrs["gen_ai.system_instructions"] = encoded
	attrs["langfuse.observation.metadata.systemPrompt"] = encoded
}

func systemInstructionTexts(parsed provider.ParsedPayload) []string {
	var instructions []string
	seen := map[string]bool{}
	add := func(text string) {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || seen[trimmed] {
			return
		}
		seen[trimmed] = true
		instructions = append(instructions, text)
	}
	for _, instruction := range parsed.SystemInstructions {
		add(instruction)
	}
	for _, message := range parsed.InputMessages {
		if !isPromptLikeRole(message.Role) {
			continue
		}
		for _, text := range messageTextSegments(message) {
			add(text)
		}
	}
	return instructions
}

func messageTextSegments(message provider.Message) []string {
	if len(message.Parts) > 0 {
		var texts []string
		for _, part := range message.Parts {
			if part.Type != "text" {
				continue
			}
			if text, ok := part.Content.(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		}
		return texts
	}
	if text, ok := message.Content.(string); ok && strings.TrimSpace(text) != "" {
		return []string{text}
	}
	return nil
}

func isPromptLikeRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return true
	default:
		return false
	}
}

func langfuseDisplayInput(messages []provider.Message, includeSystem bool) any {
	if includeSystem {
		return messages
	}
	displayMessages := make([]provider.Message, 0, len(messages))
	filteredPrompt := false
	for _, message := range messages {
		if isPromptLikeRole(message.Role) {
			filteredPrompt = true
			continue
		}
		displayMessages = append(displayMessages, message)
	}
	if len(displayMessages) > 0 {
		return displayMessages
	}
	if filteredPrompt {
		return map[string]any{
			"status":                    "input_filtered",
			"reason":                    "system/developer prompt hidden from repeated Langfuse display input",
			"system_prompt_in_metadata": true,
		}
	}
	return messages
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

func responseStatus(response traceRequestRow, parsedResponse provider.ParsedPayload) otel.Status {
	status := otel.Status{}
	if isGenerationErrorResponse(response, parsedResponse) {
		status.Code = "STATUS_CODE_ERROR"
		if message := generationErrorMessage(response, parsedResponse); message != "" {
			status.Message = message
		}
	} else {
		status.Code = "STATUS_CODE_OK"
	}
	return status
}

func isErrorResponse(response traceRequestRow) bool {
	return response.Outcome == "error" || (response.HTTPStatus.Valid && response.HTTPStatus.Int64 >= 400)
}

func isGenerationErrorResponse(response traceRequestRow, parsedResponse provider.ParsedPayload) bool {
	return isErrorResponse(response) || parsedFinishReasonIsError(parsedResponse)
}

func parsedFinishReasonIsError(parsedResponse provider.ParsedPayload) bool {
	for _, reason := range parsedResponse.FinishReasons {
		switch strings.ToLower(strings.TrimSpace(reason)) {
		case "error", "failed", "failure":
			return true
		}
	}
	return false
}

func rawGenerationOutcome(isError bool) string {
	if isError {
		return "error"
	}
	return "ok"
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

func generationErrorMessage(response traceRequestRow, parsedResponse provider.ParsedPayload) string {
	return displayErrorMessage(rawGenerationErrorMessage(response, parsedResponse))
}

func rawGenerationErrorMessage(response traceRequestRow, parsedResponse provider.ParsedPayload) string {
	if response.ErrorMessage.Valid && response.ErrorMessage.String != "" {
		return response.ErrorMessage.String
	}
	if message := parsedResponseErrorMessage(parsedResponse); message != "" {
		return message
	}
	if response.HTTPStatus.Valid {
		return fmt.Sprintf("HTTP %d", response.HTTPStatus.Int64)
	}
	return ""
}

func displayErrorMessage(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sanitized := volatileObjectRefPattern.ReplaceAllString(raw, "")
	sanitized = strings.Join(strings.Fields(sanitized), " ")
	if sanitized == "" {
		return raw
	}
	return sanitized
}

func parsedResponseErrorMessage(parsedResponse provider.ParsedPayload) string {
	for _, message := range parsedResponse.OutputMessages {
		for _, part := range message.Parts {
			if part.Type == "text" {
				if text, ok := part.Content.(string); ok && text != "" {
					return text
				}
			}
		}
		if text, ok := message.Content.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func errorObservationOutput(response traceRequestRow, errorType string, parsedResponse provider.ParsedPayload) map[string]any {
	output := map[string]any{
		"status":     "error",
		"error_type": errorType,
	}
	rawMessage := rawGenerationErrorMessage(response, parsedResponse)
	message := displayErrorMessage(rawMessage)
	if message != "" {
		output["message"] = message
	}
	if rawMessage != "" && rawMessage != message {
		output["raw_message"] = rawMessage
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
