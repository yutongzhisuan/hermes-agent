package metrics_test

import (
	"strings"
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/metrics"
)

func TestRenderPrometheusCounters(t *testing.T) {
	metrics.Reset()
	metrics.Inc("relay_tasks_dispatched_total", map[string]string{"status": "pending", "batch": "false"}, 1)
	body := metrics.RenderPrometheus()
	if !strings.Contains(body, "# TYPE relay_tasks_dispatched_total counter") {
		t.Fatalf("missing counter type: %s", body)
	}
	if !strings.Contains(body, `relay_tasks_dispatched_total{batch="false",status="pending"} 1`) {
		t.Fatalf("missing counter value: %s", body)
	}
}
