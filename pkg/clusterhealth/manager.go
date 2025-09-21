package clusterhealth

import (
	"context"
	"time"
)

// Report captures the context associated with a health status update for the
// local node.
type Report struct {
	Stage  string
	Reason string
}

// Record represents the persisted health state for a node in the cluster.
type Record struct {
	Node       string
	Healthy    bool
	Stage      string
	Reason     string
	ReportedAt time.Time
}

// Manager exposes the cluster-level health coordination contract required by
// the orchestrator.  Implementations persist unhealthy node signals so other
// nodes can block their own reboots when the cluster is degraded.
type Manager interface {
	// ReportHealthy records a healthy status for the local node.
	ReportHealthy(ctx context.Context) error
	// ReportUnhealthy stores an unhealthy marker for the local node.  The
	// supplied report metadata is persisted alongside the marker for
	// observability.
	ReportUnhealthy(ctx context.Context, report Report) error
	// Status returns the last reported health records for nodes in the
	// cluster.  Callers are expected to treat the returned slice as
	// read-only.
	Status(ctx context.Context) ([]Record, error)
}
