// Package metrics is the v1 Prometheus contract of the service: a small
// thread-safe registry that renders the mandatory metric families in the
// Prometheus text exposition format.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// DefaultBuckets matches the Prometheus default histogram buckets.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry owns every metric family of one process.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{families: make(map[string]*family)}
}

type family struct {
	name       string
	help       string
	metricType string
	labelNames []string
	mu         sync.Mutex
	values     map[string]*value
	buckets    []float64
}

type value struct {
	labels     []string
	number     float64
	histogram  []uint64
	sum        float64
	count      uint64
	hasHistory bool
}

func (r *Registry) family(name, help, metricType string, labelNames []string) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.families[name]; ok {
		return existing
	}
	f := &family{
		name:       name,
		help:       help,
		metricType: metricType,
		labelNames: labelNames,
		values:     make(map[string]*value),
	}
	r.families[name] = f
	return f
}

// Counter is a monotonically increasing metric.
type Counter struct{ f *family }

// Counter registers a counter family.
func (r *Registry) Counter(name, help string, labelNames ...string) *Counter {
	return &Counter{f: r.family(name, help, "counter", labelNames)}
}

// Inc increments the sample identified by the label values.
func (c *Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

// Add adds delta to the sample identified by the label values.
func (c *Counter) Add(delta float64, labelValues ...string) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.sample(labelValues).number += delta
}

// Gauge is a metric that can go up and down.
type Gauge struct{ f *family }

// Gauge registers a gauge family.
func (r *Registry) Gauge(name, help string, labelNames ...string) *Gauge {
	return &Gauge{f: r.family(name, help, "gauge", labelNames)}
}

// Set stores the sample value.
func (g *Gauge) Set(number float64, labelValues ...string) {
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	g.f.sample(labelValues).number = number
}

// Histogram observes durations.
type Histogram struct{ f *family }

// Histogram registers a histogram family.
func (r *Registry) Histogram(name, help string, labelNames ...string) *Histogram {
	f := r.family(name, help, "histogram", labelNames)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.buckets == nil {
		f.buckets = DefaultBuckets
	}
	return &Histogram{f: f}
}

// Observe records one observation.
func (h *Histogram) Observe(seconds float64, labelValues ...string) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	sample := h.f.sample(labelValues)
	if !sample.hasHistory {
		sample.histogram = make([]uint64, len(h.f.buckets))
		sample.hasHistory = true
	}
	for i, bound := range h.f.buckets {
		if seconds <= bound {
			sample.histogram[i]++
		}
	}
	sample.sum += seconds
	sample.count++
}

func (f *family) sample(labelValues []string) *value {
	key := strings.Join(labelValues, "\x00")
	if existing, ok := f.values[key]; ok {
		return existing
	}
	sample := &value{labels: append([]string(nil), labelValues...)}
	f.values[key] = sample
	return sample
}

// Render writes the registry in the Prometheus text exposition format.
func (r *Registry) Render(w io.Writer) error {
	r.mu.Lock()
	families := make([]*family, 0, len(r.families))
	for _, f := range r.families {
		families = append(families, f)
	}
	r.mu.Unlock()
	sort.Slice(families, func(i, j int) bool { return families[i].name < families[j].name })

	for _, f := range families {
		if err := f.render(w); err != nil {
			return err
		}
	}
	return nil
}

func (f *family) render(w io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.metricType); err != nil {
		return err
	}
	keys := make([]string, 0, len(f.values))
	for key := range f.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sample := f.values[key]
		if f.metricType == "histogram" {
			if err := f.renderHistogram(w, sample); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s%s %s\n", f.name, f.labels(sample.labels), formatFloat(sample.number)); err != nil {
			return err
		}
	}
	return nil
}

func (f *family) renderHistogram(w io.Writer, sample *value) error {
	for i, bound := range f.buckets {
		labels := append(append([]string{}, sample.labels...), formatFloat(bound))
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, f.labelsWithExtra(labels, "le"), sample.histogram[i]); err != nil {
			return err
		}
	}
	labels := append(append([]string{}, sample.labels...), "+Inf")
	if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, f.labelsWithExtra(labels, "le"), sample.count); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s_sum%s %s\n", f.name, f.labels(sample.labels), formatFloat(sample.sum)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s_count%s %d\n", f.name, f.labels(sample.labels), sample.count)
	return err
}

func (f *family) labels(labelValues []string) string {
	return f.renderLabels(f.labelNames, labelValues)
}

func (f *family) labelsWithExtra(labelValues []string, extraName string) string {
	return f.renderLabels(append(append([]string{}, f.labelNames...), extraName), labelValues)
}

func (f *family) renderLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, 0, len(names))
	for i, name := range names {
		labelValue := ""
		if i < len(values) {
			labelValue = values[i]
		}
		parts = append(parts, fmt.Sprintf(`%s="%s"`, name, escapeLabel(labelValue)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}

func formatFloat(number float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.10f", number), "0"), ".")
}

// Metrics is the v1 contract bundle consumed by the adapters.
type Metrics struct {
	// wager_transactions_total{kind,status,ingress} counts durable outcomes.
	WagerTransactionsTotal *Counter
	// wager_idempotency_duplicates_total{ingress} counts replayed commands.
	WagerIdempotencyDuplicatesTotal *Counter
	// wager_retries_total{worker,reason} counts recoverable worker retries.
	WagerRetriesTotal *Counter
	// wager_dlq_total{reason} counts messages abandoned to redrive.
	WagerDLQTotal *Counter
	// wallet_concurrency_conflicts_total counts unique-collision retries.
	WalletConcurrencyConflictsTotal *Counter
	// outbox_oldest_pending_seconds is the age of the oldest unpublished event.
	OutboxOldestPendingSeconds *Gauge
	// wager_processing_duration_seconds{ingress,kind,status} observes handling latency.
	WagerProcessingDurationSeconds *Histogram
	// wallet_reconciliation_divergences_total counts nonzero differences.
	WalletReconciliationDivergencesTotal *Counter
}

// New builds the v1 metric bundle over one registry.
func New(registry *Registry) *Metrics {
	return &Metrics{
		WagerTransactionsTotal:               registry.Counter("wager_transactions_total", "Durable wager transaction outcomes.", "kind", "status", "ingress"),
		WagerIdempotencyDuplicatesTotal:      registry.Counter("wager_idempotency_duplicates_total", "Idempotent replays served.", "ingress"),
		WagerRetriesTotal:                    registry.Counter("wager_retries_total", "Recoverable worker retries.", "worker", "reason"),
		WagerDLQTotal:                        registry.Counter("wager_dlq_total", "Messages abandoned to the dead-letter queue.", "reason"),
		WalletConcurrencyConflictsTotal:      registry.Counter("wallet_concurrency_conflicts_total", "Concurrent write conflicts observed on wallet operations."),
		OutboxOldestPendingSeconds:           registry.Gauge("outbox_oldest_pending_seconds", "Age in seconds of the oldest unpublished outbox event."),
		WagerProcessingDurationSeconds:       registry.Histogram("wager_processing_duration_seconds", "Wager processing duration in seconds.", "ingress", "kind", "status"),
		WalletReconciliationDivergencesTotal: registry.Counter("wallet_reconciliation_divergences_total", "Wallet reconciliations that detected a nonzero difference."),
	}
}
