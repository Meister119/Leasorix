package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// HA holds the high availability metrics. A nil *HA is valid and records
// nothing.
type HA struct {
	state       *prometheus.GaugeVec
	connected   prometheus.Gauge
	replicated  *prometheus.CounterVec
	replErrors  prometheus.Counter
	replLatency prometheus.Histogram
	conflicts   prometheus.Counter
	resyncs     *prometheus.CounterVec
	configVer   prometheus.Gauge
	certExpiry  prometheus.Gauge
}

// HAStates are the label values of leasorix_ha_state.
var HAStates = []string{"NORMAL", "COMMUNICATION_INTERRUPTED", "PARTNER_DOWN", "RECOVER", "RECOVER_DONE"}

// NewHA registers the HA metrics.
func (r *Registry) NewHA() *HA {
	m := &HA{
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "state",
			Help: "Failover state of this server (1 for the current state).",
		}, []string{"state"}),
		connected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "partner_connected",
			Help: "1 if heartbeats with the partner succeed.",
		}),
		replicated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "lease_updates_total",
			Help: "Lease updates exchanged with the partner.",
		}, []string{"direction"}),
		replErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "replication_errors_total",
			Help: "Lease update batches that could not be delivered to the partner.",
		}),
		replLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "replication_duration_seconds",
			Help:    "Round trip time of lease update batches.",
			Buckets: prometheus.ExponentialBuckets(0.0002, 2.5, 10),
		}),
		conflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "lease_conflicts_total",
			Help: "Lease records of different clients for the same address resolved during replication.",
		}),
		resyncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "resyncs_total",
			Help: "Lease table resynchronisations with the partner.",
		}, []string{"result"}),
		configVer: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "config_version",
			Help: "Configuration version (leader: current, partner: applied).",
		}),
		certExpiry: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "ha", Name: "certificate_expiry_timestamp_seconds",
			Help: "Expiry of the node certificate used for the peer connection (mtls).",
		}),
	}
	r.Register(m.state, m.connected, m.replicated, m.replErrors, m.replLatency, m.conflicts, m.resyncs, m.configVer, m.certExpiry)
	return m
}

// State sets the current failover state.
func (m *HA) State(s string) {
	if m == nil {
		return
	}
	for _, v := range HAStates {
		val := 0.0
		if v == s {
			val = 1
		}
		m.state.WithLabelValues(v).Set(val)
	}
}

// Connected sets the partner connection gauge.
func (m *HA) Connected(ok bool) {
	if m == nil {
		return
	}
	if ok {
		m.connected.Set(1)
	} else {
		m.connected.Set(0)
	}
}

// Replicated counts lease updates sent ("out") or received ("in").
func (m *HA) Replicated(direction string, n int) {
	if m == nil {
		return
	}
	m.replicated.WithLabelValues(direction).Add(float64(n))
}

// Push records one lease update batch sent to the partner.
func (m *HA) Push(d time.Duration, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.replErrors.Inc()
		return
	}
	m.replLatency.Observe(d.Seconds())
}

// Conflict counts a resolved lease conflict.
func (m *HA) Conflict() {
	if m != nil {
		m.conflicts.Inc()
	}
}

// Resync counts a resync ("ok" or "error").
func (m *HA) Resync(result string) {
	if m != nil {
		m.resyncs.WithLabelValues(result).Inc()
	}
}

// ConfigVersion sets the configuration version gauge.
func (m *HA) ConfigVersion(v uint64) {
	if m != nil {
		m.configVer.Set(float64(v))
	}
}

// CertExpiry sets the certificate expiry gauge.
func (m *HA) CertExpiry(t time.Time) {
	if m != nil && !t.IsZero() {
		m.certExpiry.Set(float64(t.Unix()))
	}
}
