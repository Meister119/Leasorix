package metrics

import "github.com/prometheus/client_golang/prometheus"

// DNS holds the dynamic DNS update metrics. A nil *DNS is valid and records
// nothing.
type DNS struct {
	updates       *prometheus.CounterVec
	registrations *prometheus.GaugeVec
}

// DNSStates are the label values of leasorix_dns_registrations.
var DNSStates = []string{"registered", "pending", "conflict", "error"}

// NewDNS registers the DNS update metrics.
func (r *Registry) NewDNS() *DNS {
	m := &DNS{
		updates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dns", Name: "updates_total",
			Help: "DNS updates sent, by record type (A, PTR), operation (add, remove) and result (success, conflict, error).",
		}, []string{"record", "operation", "result"}),
		registrations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "dns", Name: "registrations",
			Help: "Addresses with DNS registrations by state.",
		}, []string{"state"}),
	}
	r.Register(m.updates, m.registrations)
	for _, s := range DNSStates {
		m.registrations.WithLabelValues(s).Set(0)
	}
	return m
}

// Update counts one update.
func (m *DNS) Update(record, operation, result string) {
	if m != nil {
		m.updates.WithLabelValues(record, operation, result).Inc()
	}
}

// Registrations sets the registration gauges.
func (m *DNS) Registrations(counts map[string]int) {
	if m == nil {
		return
	}
	for _, s := range DNSStates {
		m.registrations.WithLabelValues(s).Set(float64(counts[s]))
	}
}
