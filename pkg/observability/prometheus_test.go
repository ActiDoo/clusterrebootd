package observability

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func TestPrometheusCollectorCounter(t *testing.T) {
	collector := NewPrometheusCollector()
	collector.Collect(Metric{
		Name:        "orchestration_outcomes_total",
		Type:        MetricCounter,
		Value:       2,
		Labels:      map[string]string{"status": "ready"},
		Description: "Number of orchestrator outcomes",
	})
	collector.Collect(Metric{
		Name:   "orchestration_outcomes_total",
		Type:   MetricCounter,
		Value:  1,
		Labels: map[string]string{"status": "ready"},
	})

	mfs, err := collector.Registry().Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	metric := findMetric(t, mfs, "clusterrebootd_orchestration_outcomes_total")
	if len(metric.Metric) != 1 {
		t.Fatalf("expected single metric sample, got %d", len(metric.Metric))
	}
	sample := metric.Metric[0]
	if got := sample.GetCounter().GetValue(); got != 3 {
		t.Fatalf("expected counter value 3, got %v", got)
	}
	labels := sample.GetLabel()
	if len(labels) != 1 || labels[0].GetName() != "status" || labels[0].GetValue() != "ready" {
		t.Fatalf("unexpected labels: %+v", labels)
	}
}

func TestPrometheusCollectorHistogram(t *testing.T) {
	collector := NewPrometheusCollector()
	collector.Collect(Metric{
		Name:        "health_check_seconds",
		Type:        MetricHistogram,
		Value:       1.5,
		Labels:      map[string]string{"phase": "pre-lock", "result": "pass"},
		Description: "health duration",
		Unit:        "seconds",
	})
	collector.Collect(Metric{
		Name:   "health_check_seconds",
		Type:   MetricHistogram,
		Value:  2.5,
		Labels: map[string]string{"phase": "pre-lock", "result": "pass"},
	})

	mfs, err := collector.Registry().Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	metric := findMetric(t, mfs, "clusterrebootd_health_check_seconds")
	if len(metric.Metric) != 1 {
		t.Fatalf("expected single histogram sample, got %d", len(metric.Metric))
	}
	mfSample := metric.Metric[0]
	sample := mfSample.GetHistogram()
	if got := sample.GetSampleCount(); got != 2 {
		t.Fatalf("expected sample count 2, got %v", got)
	}
	if got := sample.GetSampleSum(); got < 4.0 || got > 4.1 {
		t.Fatalf("expected sum close to 4.0, got %v", got)
	}
	labels := mfSample.GetLabel()
	if len(labels) == 0 {
		t.Fatalf("expected histogram labels to include unit, got none")
	}
	var foundUnit bool
	for _, label := range labels {
		if label.GetName() == "unit" && label.GetValue() == "seconds" {
			foundUnit = true
		}
	}
	if !foundUnit {
		t.Fatalf("expected unit label to be recorded, got %+v", labels)
	}
}

func TestPrometheusCollectorIgnoresMismatchedLabels(t *testing.T) {
	collector := NewPrometheusCollector()
	collector.Collect(Metric{
		Name:   "lock_attempts_total",
		Type:   MetricCounter,
		Value:  1,
		Labels: map[string]string{"result": "success"},
	})
	// Attempt to record with a different set of labels; collector should ignore to avoid panics.
	collector.Collect(Metric{
		Name:   "lock_attempts_total",
		Type:   MetricCounter,
		Value:  1,
		Labels: map[string]string{"result": "success", "node": "node-a"},
	})

	mfs, err := collector.Registry().Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	metric := findMetric(t, mfs, "clusterrebootd_lock_attempts_total")
	if len(metric.Metric) != 1 {
		t.Fatalf("expected single metric after mismatch attempt, got %d", len(metric.Metric))
	}
	sample := metric.Metric[0]
	if got := sample.GetCounter().GetValue(); got != 1 {
		t.Fatalf("expected counter value 1 after ignoring mismatched labels, got %v", got)
	}
}

func TestPrometheusCollectorHandler(t *testing.T) {
	collector := NewPrometheusCollector()
	handler := collector.Handler()
	if handler == nil {
		t.Fatal("expected handler not nil")
	}
}

// findMetric searches metric families by name.
func findMetric(t *testing.T, mfs []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf
		}
	}
	t.Fatalf("metric %s not found", name)
	return nil
}
