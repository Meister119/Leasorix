package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DHCP4 holds the packet level DHCPv4 metrics. A nil *DHCP4 is valid and
// records nothing (used in tests).
type DHCP4 struct {
	received *prometheus.CounterVec
	sent     *prometheus.CounterVec
	dropped  *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewDHCP4 creates and registers the DHCPv4 metrics.
func (r *Registry) NewDHCP4() *DHCP4 {
	m := &DHCP4{
		received: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp4", Name: "packets_received_total",
			Help: "DHCPv4 packets received by message type.",
		}, []string{"type"}),
		sent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp4", Name: "packets_sent_total",
			Help: "DHCPv4 replies sent by message type.",
		}, []string{"type"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp4", Name: "packets_dropped_total",
			Help: "DHCPv4 packets answered with silence, by reason.",
		}, []string{"reason"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp4", Name: "errors_total",
			Help: "Internal errors while processing DHCPv4 packets, by kind.",
		}, []string{"kind"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "dhcp4", Name: "request_duration_seconds",
			Help:    "Time to process a DHCPv4 packet (including conflict probe and lease commit).",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}, []string{"type"}),
	}
	r.Register(m.received, m.sent, m.dropped, m.errors, m.duration)
	return m
}

// Received counts an incoming packet.
func (m *DHCP4) Received(msgType string) {
	if m != nil {
		m.received.WithLabelValues(msgType).Inc()
	}
}

// Sent counts an outgoing reply.
func (m *DHCP4) Sent(msgType string) {
	if m != nil {
		m.sent.WithLabelValues(msgType).Inc()
	}
}

// Dropped counts a packet that is deliberately not answered.
func (m *DHCP4) Dropped(reason string) {
	if m != nil {
		m.dropped.WithLabelValues(reason).Inc()
	}
}

// Error counts an internal error.
func (m *DHCP4) Error(kind string) {
	if m != nil {
		m.errors.WithLabelValues(kind).Inc()
	}
}

// Observe records the processing time of a packet.
func (m *DHCP4) Observe(msgType string, d time.Duration) {
	if m != nil {
		m.duration.WithLabelValues(msgType).Observe(d.Seconds())
	}
}

// Leases holds lease store metrics. A nil *Leases records nothing.
type Leases struct {
	events      *prometheus.CounterVec
	commit      prometheus.Histogram
	batch       prometheus.Histogram
	commitError prometheus.Counter
}

// NewLeases creates and registers the lease metrics.
func (r *Registry) NewLeases() *Leases {
	m := &Leases{
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "lease", Name: "events_total",
			Help: "Lease events: assign, renew, release, decline, conflict, expire, nak.",
		}, []string{"event"}),
		commit: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "lease", Name: "commit_duration_seconds",
			Help:    "Duration of a lease store group commit (including fsync).",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
		}),
		batch: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "lease", Name: "commit_batch_size",
			Help:    "Number of requests combined into one group commit.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512},
		}),
		commitError: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "lease", Name: "commit_errors_total",
			Help: "Failed lease store commits.",
		}),
	}
	r.Register(m.events, m.commit, m.batch, m.commitError)
	return m
}

// Event counts a lease event.
func (m *Leases) Event(name string) {
	if m != nil {
		m.events.WithLabelValues(name).Inc()
	}
}

// Commit records a group commit.
func (m *Leases) Commit(d time.Duration, requests int, err error) {
	if m == nil {
		return
	}
	m.commit.Observe(d.Seconds())
	m.batch.Observe(float64(requests))
	if err != nil {
		m.commitError.Inc()
	}
}

// ScopeSample is one scope's utilisation for the scrape-time collector.
type ScopeSample struct {
	Scope       string
	Subnet      string
	Usable      uint64
	InUse       uint64
	Reserved    uint64
	Declined    uint64
	Available   uint64
	Utilization float64 // 0..1
	ByState     map[string]uint64
}

type scopeCollector struct {
	fn          func() []ScopeSample
	addresses   *prometheus.Desc
	utilization *prometheus.Desc
	leases      *prometheus.Desc
}

// RegisterScopeCollector exposes per scope statistics computed at scrape
// time by fn.
func (r *Registry) RegisterScopeCollector(fn func() []ScopeSample) {
	r.Register(&scopeCollector{
		fn: fn,
		addresses: prometheus.NewDesc(Namespace+"_scope_addresses",
			"Addresses of the dynamic range by kind (usable, in_use, reserved, declined, available).",
			[]string{"scope", "subnet", "kind"}, nil),
		utilization: prometheus.NewDesc(Namespace+"_scope_utilization_ratio",
			"Share of usable addresses that cannot be handed out (0..1).",
			[]string{"scope", "subnet"}, nil),
		leases: prometheus.NewDesc(Namespace+"_leases",
			"Lease records by state.",
			[]string{"scope", "subnet", "state"}, nil),
	})
}

func (c *scopeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.addresses
	ch <- c.utilization
	ch <- c.leases
}

func (c *scopeCollector) Collect(ch chan<- prometheus.Metric) {
	for _, s := range c.fn() {
		for kind, v := range map[string]uint64{
			"usable": s.Usable, "in_use": s.InUse, "reserved": s.Reserved, "declined": s.Declined, "available": s.Available,
		} {
			ch <- prometheus.MustNewConstMetric(c.addresses, prometheus.GaugeValue, float64(v), s.Scope, s.Subnet, kind)
		}
		ch <- prometheus.MustNewConstMetric(c.utilization, prometheus.GaugeValue, s.Utilization, s.Scope, s.Subnet)
		for _, state := range []string{"active", "expired", "released", "declined"} {
			ch <- prometheus.MustNewConstMetric(c.leases, prometheus.GaugeValue, float64(s.ByState[state]), s.Scope, s.Subnet, state)
		}
	}
}
