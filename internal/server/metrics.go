package server

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Registry      *prometheus.Registry
	RequestsTotal *prometheus.CounterVec
	ChecksTotal   *prometheus.CounterVec
	CheckDuration *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		Registry: registry,
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hostcheck_requests_total",
				Help: "Total number of HTTP requests by endpoint and status.",
			},
			[]string{"endpoint", "status"},
		),
		ChecksTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hostcheck_checks_total",
				Help: "Total number of checks executed by check name and status.",
			},
			[]string{"check_name", "status"},
		),
		CheckDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "hostcheck_check_duration_seconds",
				Help:    "Duration of check execution in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"check_name"},
		),
	}

	registry.MustRegister(
		m.RequestsTotal,
		m.CheckDuration,
		m.ChecksTotal,
	)

	return m
}
