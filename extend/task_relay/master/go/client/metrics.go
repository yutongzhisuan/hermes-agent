package client

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Master SDK Prometheus collectors.
type Metrics struct {
	RPCTotal       *prometheus.CounterVec
	RPCDuration    *prometheus.HistogramVec
	DispatchesTotal *prometheus.CounterVec
}

// NewMetrics registers Master SDK metrics on the provided registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		RPCTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_master_rpc_total",
			Help: "Total Master SDK gRPC calls",
		}, []string{"method", "status"}),
		RPCDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relay_master_rpc_seconds",
			Help:    "Master SDK gRPC call latency in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		DispatchesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_tasks_dispatched_total",
			Help: "Total tasks dispatched by the Master SDK",
		}, []string{"status", "batch"}),
	}
	reg.MustRegister(m.RPCTotal, m.RPCDuration, m.DispatchesTotal)
	return m
}

func (m *Metrics) observeRPC(method string, err error, elapsed time.Duration) {
	if m == nil {
		return
	}
	status := "ok"
	if err != nil {
		status = "error"
	}
	m.RPCTotal.WithLabelValues(method, status).Inc()
	m.RPCDuration.WithLabelValues(method).Observe(elapsed.Seconds())
}

func (m *Metrics) incDispatch(status string, batch bool) {
	if m == nil {
		return
	}
	batchLabel := "false"
	if batch {
		batchLabel = "true"
	}
	m.DispatchesTotal.WithLabelValues(status, batchLabel).Inc()
}
