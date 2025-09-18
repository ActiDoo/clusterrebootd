package orchestrator

import (
	"context"

	"github.com/clusterrebootd/clusterrebootd/pkg/observability"
)

// Reporter consumes orchestration events and metrics for logging or aggregation.
type Reporter interface {
	RecordEvent(context.Context, observability.Event)
	RecordMetric(observability.Metric)
}

// ReporterFuncs wires plain functions into a Reporter implementation.
type ReporterFuncs struct {
	OnEvent  func(context.Context, observability.Event)
	OnMetric func(observability.Metric)
}

// RecordEvent implements Reporter.
func (r ReporterFuncs) RecordEvent(ctx context.Context, event observability.Event) {
	if r.OnEvent != nil {
		r.OnEvent(ctx, event)
	}
}

// RecordMetric implements Reporter.
func (r ReporterFuncs) RecordMetric(metric observability.Metric) {
	if r.OnMetric != nil {
		r.OnMetric(metric)
	}
}

// NoopReporter discards all events and metrics.
type NoopReporter struct{}

// RecordEvent implements Reporter.
func (NoopReporter) RecordEvent(context.Context, observability.Event) {}

// RecordMetric implements Reporter.
func (NoopReporter) RecordMetric(observability.Metric) {}

// StructuredReporter forwards events to the provided logger and metrics collector.
type StructuredReporter struct {
	node      string
	component string
	logger    observability.Logger
	metrics   observability.MetricsCollector
}

// NewStructuredReporter builds a reporter that enriches events with node and component context.
func NewStructuredReporter(nodeName string, logger observability.Logger, metrics observability.MetricsCollector) *StructuredReporter {
	return &StructuredReporter{
		node:      nodeName,
		component: "orchestrator",
		logger:    logger,
		metrics:   metrics,
	}
}

// RecordEvent implements Reporter.
func (r *StructuredReporter) RecordEvent(ctx context.Context, event observability.Event) {
	if r == nil || r.logger == nil {
		return
	}
	cloned := event.Clone()
	if cloned.Node == "" {
		cloned.Node = r.node
	}
	if cloned.Component == "" {
		cloned.Component = r.component
	}
	_ = r.logger.Log(ctx, cloned)
}

// RecordMetric implements Reporter.
func (r *StructuredReporter) RecordMetric(metric observability.Metric) {
	if r == nil || r.metrics == nil {
		return
	}
	r.metrics.Collect(metric)
}

var _ Reporter = ReporterFuncs{}
var _ Reporter = NoopReporter{}
var _ Reporter = (*StructuredReporter)(nil)
