package metrics

import "github.com/prometheus/client_golang/prometheus"

// DHCP6 holds the DHCPv6 metrics. A nil *DHCP6 is valid and records nothing.
type DHCP6 struct {
	received *prometheus.CounterVec
	sent     *prometheus.CounterVec
	dropped  *prometheus.CounterVec
	events   *prometheus.CounterVec
}

// NewDHCP6 registers the DHCPv6 metrics.
func (r *Registry) NewDHCP6() *DHCP6 {
	m := &DHCP6{
		received: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp6", Name: "packets_received_total",
			Help: "DHCPv6 messages received, by message type (relayed messages by their inner type).",
		}, []string{"type"}),
		sent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp6", Name: "packets_sent_total",
			Help: "DHCPv6 messages sent, by message type.",
		}, []string{"type"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp6", Name: "packets_dropped_total",
			Help: "DHCPv6 messages not answered, by reason.",
		}, []string{"reason"}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "dhcp6", Name: "lease_events_total",
			Help: "DHCPv6 binding events (assign, renew, release, decline, expire, delete, no_address).",
		}, []string{"event"}),
	}
	r.Register(m.received, m.sent, m.dropped, m.events)
	return m
}

// Received counts a received message.
func (m *DHCP6) Received(t string) {
	if m != nil {
		m.received.WithLabelValues(t).Inc()
	}
}

// Sent counts a sent message.
func (m *DHCP6) Sent(t string) {
	if m != nil {
		m.sent.WithLabelValues(t).Inc()
	}
}

// Dropped counts a message that is not answered.
func (m *DHCP6) Dropped(reason string) {
	if m != nil {
		m.dropped.WithLabelValues(reason).Inc()
	}
}

// LeaseEvent counts a binding event.
func (m *DHCP6) LeaseEvent(event string) {
	if m != nil {
		m.events.WithLabelValues(event).Inc()
	}
}
