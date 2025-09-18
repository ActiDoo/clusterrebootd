package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestJSONLoggerEmitsEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf)
	logger.now = func() time.Time { return time.Unix(100, 0).UTC() }

	event := Event{
		Level:   LevelInfo,
		Node:    "node-a",
		Event:   "detectors_evaluated",
		Message: "detectors executed",
		Fields: map[string]interface{}{
			"phase":    "pre-lock",
			"requires": false,
		},
	}

	if err := logger.Log(context.Background(), event); err != nil {
		t.Fatalf("log event: %v", err)
	}

	var payload Event
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload.Timestamp.Unix() != 100 {
		t.Fatalf("expected timestamp to be set, got %v", payload.Timestamp)
	}
	if payload.Level != LevelInfo {
		t.Fatalf("unexpected level: %s", payload.Level)
	}
	if payload.Event != event.Event {
		t.Fatalf("unexpected event name: %s", payload.Event)
	}
	if payload.Fields["phase"] != "pre-lock" {
		t.Fatalf("expected phase field preserved, got %v", payload.Fields)
	}
}

func TestJSONLoggerRequiresWriter(t *testing.T) {
	logger := NewJSONLogger(nil)
	if err := logger.Log(context.Background(), Event{Event: "test"}); err == nil {
		t.Fatal("expected error when writer is nil")
	}
}
