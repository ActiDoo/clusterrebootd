package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Logger emits structured events to an underlying sink.
type Logger interface {
	Log(context.Context, Event) error
}

// LoggerFunc adapts a function into a Logger.
type LoggerFunc func(context.Context, Event) error

// Log implements Logger.
func (f LoggerFunc) Log(ctx context.Context, event Event) error {
	return f(ctx, event)
}

// JSONLogger writes each event as a single JSON object on its own line.
type JSONLogger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewJSONLogger builds a JSONLogger writing to the provided io.Writer.
func NewJSONLogger(w io.Writer) *JSONLogger {
	return &JSONLogger{w: w, now: time.Now}
}

// Log implements Logger by emitting a JSON representation of the event.
func (l *JSONLogger) Log(_ context.Context, event Event) error {
	if l == nil || l.w == nil {
		return fmt.Errorf("json logger is not configured")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if event.Timestamp.IsZero() {
		event.Timestamp = l.now()
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if _, err := l.w.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}
