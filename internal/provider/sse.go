package provider

import (
	"strings"

	"github.com/zenfun/agelish-teacher/internal/jsonx"
)

type sseEvent struct {
	Event string
	Data  any
}

func parseSSE(raw []byte) []sseEvent {
	text := string(raw)
	var events []sseEvent
	currentEvent := ""
	var dataLines []string

	flush := func() {
		if len(dataLines) == 0 {
			currentEvent = ""
			return
		}
		joined := strings.Join(dataLines, "\n")
		var data any
		if joined == "[DONE]" {
			data = joined
		} else if err := jsonx.Unmarshal([]byte(joined), &data); err != nil {
			data = joined
		}
		eventName := currentEvent
		if eventName == "" {
			eventName = "message"
		}
		events = append(events, sseEvent{Event: eventName, Data: data})
		currentEvent = ""
		dataLines = nil
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" {
			flush()
		}
	}
	flush()
	return events
}

func eventObject(event sseEvent) (map[string]any, bool) {
	data, ok := event.Data.(map[string]any)
	return data, ok
}

func eventType(event sseEvent, data map[string]any) string {
	if raw, ok := data["type"].(string); ok && raw != "" {
		return raw
	}
	return event.Event
}
