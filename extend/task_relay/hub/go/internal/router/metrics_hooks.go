package router

import "github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/metrics"

func recordDispatched(batch bool) {
	metrics.Inc("relay_tasks_dispatched_total", map[string]string{
		"status": "pending",
		"batch":  boolLabel(batch),
	}, 1)
}

func recordClaimed() {
	metrics.Inc("relay_tasks_claimed_total", nil, 1)
}

func recordTerminal(status string) {
	metrics.Inc("relay_tasks_terminal_total", map[string]string{"status": status}, 1)
}

func observeTaskLatency(status, workerID string, seconds float64) {
	metrics.Observe("relay_task_latency_seconds", map[string]string{
		"status":    status,
		"worker_id": workerID,
	}, seconds)
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
