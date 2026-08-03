package client_test

import (
	"testing"

	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMetricsRegistersCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := client.NewMetrics(reg)
	metrics.RPCTotal.WithLabelValues("DispatchTask", "ok").Inc()
	metrics.DispatchesTotal.WithLabelValues("TASK_STATUS_PENDING", "false").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(families) < 2 {
		t.Fatalf("expected metrics families, got %d", len(families))
	}
}

func TestTLSConfigValidation(t *testing.T) {
	_, err := client.LoadTransportCredentials(client.TLSConfig{
		CertFile: "/tmp/cert.pem",
	})
	if err == nil {
		t.Fatal("expected error when cert without key/ca")
	}
}
