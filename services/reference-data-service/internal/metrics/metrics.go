// Package metrics provides bounded-cardinality Prometheus metrics for
// reference-data configuration workflows.
package metrics

import (
	"time"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics implements reference.Metrics and refdata.Observer.
type Metrics struct {
	registry *prometheus.Registry

	validation  *prometheus.CounterVec
	proposals   *prometheus.CounterVec
	publication *prometheus.CounterVec
	activation  *prometheus.CounterVec
	rollback    prometheus.Histogram
	pending     prometheus.Gauge
	divergence  prometheus.Counter
	applied     prometheus.Counter
	rejected    *prometheus.CounterVec
	version     prometheus.Gauge
	stale       prometheus.Gauge
	incidents   *prometheus.CounterVec
}

// New registers reference-data metrics on the supplied registry.
func New(registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		registry: registry,
		validation: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_reference_validation_failures_total",
			Help: "Reference-data semantic validation failures by workflow stage.",
		}, []string{"section"}),
		proposals: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_reference_proposals_total",
			Help: "Reference-data proposals submitted by resulting status.",
		}, []string{"status"}),
		publication: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_reference_publications_total",
			Help: "Reference-data publication attempts by result.",
		}, []string{"result"}),
		activation: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_reference_activations_total",
			Help: "Reference-data activation outcomes.",
		}, []string{"result"}),
		rollback: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
			Name:    "nexora_reference_rollback_duration_seconds",
			Help:    "Measured reference-data rollback activation duration.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
		pending: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "nexora_reference_pending_approvals",
			Help: "Change in pending reference-data approvals.",
		}),
		divergence: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "nexora_reference_version_divergence_total",
			Help: "Detected consumer/served version divergences.",
		}),
		applied: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "nexora_reference_snapshot_applied_total",
			Help: "Snapshots atomically installed by the local consumer.",
		}),
		rejected: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_reference_snapshot_rejected_total",
			Help: "Snapshots rejected by the local consumer by reason.",
		}, []string{"reason"}),
		version: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "nexora_reference_consumer_version",
			Help: "Version currently installed by the local consumer.",
		}),
		stale: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "nexora_reference_config_stale",
			Help: "1 when the installed configuration exceeds the staleness bound.",
		}),
		incidents: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_reference_incidents_total",
			Help: "Reference-data workflow incidents by bounded category.",
		}, []string{"category"}),
	}
	return m
}

// Registry returns the metric registry.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// ValidationFailed records a non-empty validation failure set.
func (m *Metrics) ValidationFailed(section string, count int) {
	if count > 0 {
		m.validation.WithLabelValues(section).Add(float64(count))
	}
}

// ProposalSubmitted records a submitted proposal.
func (m *Metrics) ProposalSubmitted(status string) { m.proposals.WithLabelValues(status).Inc() }

// Publication records a publication result.
func (m *Metrics) Publication(result string) { m.publication.WithLabelValues(result).Inc() }

// Activation records an activation result.
func (m *Metrics) Activation(result string) { m.activation.WithLabelValues(result).Inc() }

// RollbackCompleted observes the measured elapsed rollback duration.
func (m *Metrics) RollbackCompleted(duration time.Duration) {
	m.rollback.Observe(duration.Seconds())
}

// PendingApprovals adjusts the pending approval signal.
func (m *Metrics) PendingApprovals(delta int) { m.pending.Add(float64(delta)) }

// SnapshotApplied implements refdata.Observer.
func (m *Metrics) SnapshotApplied(snapshot refdata.Snapshot) {
	m.applied.Inc()
	m.version.Set(float64(snapshot.Version))
}

// SnapshotRejected implements refdata.Observer.
func (m *Metrics) SnapshotRejected(reason string) {
	m.rejected.WithLabelValues(reason).Inc()
}

// ObserveSnapshot updates version and staleness gauges.
func (m *Metrics) ObserveSnapshot(snapshot refdata.Snapshot, now time.Time, staleAfter time.Duration) {
	m.version.Set(float64(snapshot.Version))
	if staleAfter > 0 && snapshot.Stale(now, staleAfter) {
		m.stale.Set(1)
		m.incidents.WithLabelValues("stale_config").Inc()
		return
	}
	m.stale.Set(0)
}

// Divergence records a detected version divergence.
func (m *Metrics) Divergence() { m.divergence.Inc() }

// Incident records a bounded incident category.
func (m *Metrics) Incident(category string) { m.incidents.WithLabelValues(category).Inc() }
