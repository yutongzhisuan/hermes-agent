package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/metrics"
)

func TestRenderPrometheusCounters(t *testing.T) {
	metrics.Reset()
	metrics.Inc("relay_tasks_dispatched_total", map[string]string{"status": "pending", "batch": "false"}, 1)
	body := metrics.RenderPrometheus()
	require.Contains(t, body, "# TYPE relay_tasks_dispatched_total counter")
	require.Contains(t, body, `relay_tasks_dispatched_total{batch="false",status="pending"} 1`)
}
