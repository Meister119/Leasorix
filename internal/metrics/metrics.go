// Package metrics owns the Prometheus registry of the process.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/meister119/leasorix/internal/buildinfo"
)

// Namespace prefixes every leasorix metric.
const Namespace = "leasorix"

// Registry wraps a dedicated Prometheus registry (no global state).
type Registry struct {
	reg *prometheus.Registry
}

// New creates a registry with Go runtime, process and build info collectors.
func New() *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "build_info",
		Help:      "Build information of the running leasorixd binary.",
	}, []string{"version", "commit", "goversion"})
	buildInfo.WithLabelValues(buildinfo.Version, buildinfo.Commit, buildinfo.GoVersion()).Set(1)
	reg.MustRegister(buildInfo)
	return &Registry{reg: reg}
}

// Register adds collectors to the registry. It panics on duplicate
// registration, which is a programming error.
func (r *Registry) Register(cs ...prometheus.Collector) {
	r.reg.MustRegister(cs...)
}

// Gatherer exposes the underlying gatherer (useful in tests).
func (r *Registry) Gatherer() prometheus.Gatherer { return r.reg }

// Handler serves the registry in the Prometheus exposition format.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{Registry: r.reg})
}
