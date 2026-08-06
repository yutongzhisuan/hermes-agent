package client_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/client"
)

func TestNewMetricsRegistersCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := client.NewMetrics(reg)
	metrics.RPCTotal.WithLabelValues("DispatchTask", "ok").Inc()
	metrics.DispatchesTotal.WithLabelValues("TASK_STATUS_PENDING", "false").Inc()

	families, err := reg.Gather()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(families), 2)
}

func TestTLSConfigValidation(t *testing.T) {
	_, err := client.LoadTransportCredentials(client.TLSConfig{
		CertFile: "/tmp/cert.pem",
	})
	require.Error(t, err)
}
