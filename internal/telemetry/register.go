package telemetry

import "github.com/prometheus/client_golang/prometheus"

// Register adds domain collectors to the process /metrics registry.
func Register(collectors ...prometheus.Collector) {
	registry.MustRegister(collectors...)
}
