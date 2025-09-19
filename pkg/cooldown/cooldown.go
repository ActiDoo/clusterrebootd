package cooldown

import (
	"context"
	"time"
)

// Status describes the current cooldown window for cluster reboots.
type Status struct {
	Active    bool
	Node      string
	StartedAt time.Time
	ExpiresAt time.Time
	Remaining time.Duration
}

// Manager coordinates observation and activation of reboot cooldown periods.
type Manager interface {
	// Status returns the current cooldown information. If no cooldown is
	// active the returned Status will have Active set to false.
	Status(ctx context.Context) (Status, error)
	// Start activates a new cooldown window lasting the provided duration.
	// Implementations should replace any existing window.
	Start(ctx context.Context, duration time.Duration) error
	// Close releases underlying resources. It must be safe to call multiple
	// times.
	Close() error
}
