package observability

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const prometheusNamespace = "clusterrebootd"

// PrometheusCollector translates Metric events into Prometheus metrics and exposes a registry.
type PrometheusCollector struct {
	registry        *prometheus.Registry
	mu              sync.Mutex
	counters        map[string]*prometheus.CounterVec
	counterLabels   map[string][]string
	histograms      map[string]*prometheus.HistogramVec
	histogramLabels map[string][]string
}

// NewPrometheusCollector builds a collector backed by a dedicated Prometheus registry.
func NewPrometheusCollector() *PrometheusCollector {
	return &PrometheusCollector{
		registry:        prometheus.NewRegistry(),
		counters:        make(map[string]*prometheus.CounterVec),
		counterLabels:   make(map[string][]string),
		histograms:      make(map[string]*prometheus.HistogramVec),
		histogramLabels: make(map[string][]string),
	}
}

// Collect implements MetricsCollector by forwarding the measurement into Prometheus primitives.
func (c *PrometheusCollector) Collect(metric Metric) {
	if metric.Name == "" {
		return
	}

	switch metric.Type {
	case MetricCounter:
		c.collectCounter(metric)
	case MetricHistogram:
		c.collectHistogram(metric)
	default:
		// Unknown metric types are ignored to keep the collector resilient.
	}
}

// Registry returns the underlying registry for use with HTTP handlers.
func (c *PrometheusCollector) Registry() *prometheus.Registry {
	if c == nil {
		return nil
	}
	return c.registry
}

// Handler exposes the Prometheus registry via an http.Handler.
func (c *PrometheusCollector) Handler() http.Handler {
	if c == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(c.registry, promhttp.HandlerOpts{})
}

func (c *PrometheusCollector) collectCounter(metric Metric) {
	value := metric.Value
	if value < 0 {
		value = 0
	}
	labels := cloneLabels(metric.Labels)
	labelNames := sortedKeys(labels)

	c.mu.Lock()
	defer c.mu.Unlock()

	if vec, ok := c.counters[metric.Name]; ok {
		if !equalStringSlices(c.counterLabels[metric.Name], labelNames) {
			return
		}
		vec.With(labels).Add(value)
		return
	}

	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: prometheusNamespace,
		Name:      metric.Name,
		Help:      helpText(metric),
	}, labelNames)
	if err := c.registry.Register(vec); err != nil {
		// If registration fails we skip recording to avoid panics on duplicate metrics.
		return
	}
	c.counters[metric.Name] = vec
	c.counterLabels[metric.Name] = labelNames
	vec.With(labels).Add(value)
}

func (c *PrometheusCollector) collectHistogram(metric Metric) {
	labels := cloneLabels(metric.Labels)
	labelNames := sortedKeys(labels)

	c.mu.Lock()
	defer c.mu.Unlock()

	if vec, ok := c.histograms[metric.Name]; ok {
		if !equalStringSlices(c.histogramLabels[metric.Name], labelNames) {
			return
		}
		vec.With(labels).Observe(metric.Value)
		return
	}

	opts := prometheus.HistogramOpts{
		Namespace: prometheusNamespace,
		Name:      metric.Name,
		Help:      helpText(metric),
	}
	if metric.Unit != "" {
		opts.ConstLabels = map[string]string{"unit": metric.Unit}
	}
	vec := prometheus.NewHistogramVec(opts, labelNames)
	if err := c.registry.Register(vec); err != nil {
		return
	}
	c.histograms[metric.Name] = vec
	c.histogramLabels[metric.Name] = labelNames
	vec.With(labels).Observe(metric.Value)
}

func helpText(metric Metric) string {
	if strings.TrimSpace(metric.Description) != "" {
		return metric.Description
	}
	if metric.Unit != "" {
		return metric.Name + " (" + metric.Unit + ")"
	}
	return metric.Name
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneLabels(labels map[string]string) prometheus.Labels {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(prometheus.Labels, len(labels))
	for k, v := range labels {
		cloned[k] = v
	}
	return cloned
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ MetricsCollector = (*PrometheusCollector)(nil)
