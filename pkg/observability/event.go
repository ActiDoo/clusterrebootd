package observability

import "time"

// Level represents the severity of an emitted event.
type Level string

const (
	// LevelInfo represents informational events that describe normal behaviour.
	LevelInfo Level = "info"
	// LevelWarn represents conditions that may require operator attention.
	LevelWarn Level = "warn"
	// LevelError captures failures that prevent progress.
	LevelError Level = "error"
)

// Event models a structured log entry emitted by the coordinator components.
type Event struct {
	Timestamp time.Time              `json:"ts"`
	Level     Level                  `json:"level"`
	Node      string                 `json:"node,omitempty"`
	Component string                 `json:"component,omitempty"`
	Event     string                 `json:"event"`
	Message   string                 `json:"message,omitempty"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// Clone returns a shallow copy of the event and its fields map to avoid data races
// when observers mutate their view of the metadata.
func (e Event) Clone() Event {
	clone := e
	if len(e.Fields) > 0 {
		copied := make(map[string]interface{}, len(e.Fields))
		for k, v := range e.Fields {
			copied[k] = v
		}
		clone.Fields = copied
	}
	return clone
}
