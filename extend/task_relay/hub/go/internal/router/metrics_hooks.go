package router

import "github.com/infa/task_relay/hub/internal/metrics"

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

func recordCheckpoint(workerID string) {
	metrics.Inc("relay_checkpoint_count", map[string]string{"worker_id": workerID}, 1)
}

func recordAggregateEmitted(batchID string) {
	metrics.Inc("relay_aggregate_emitted_total", map[string]string{"batch_id": batchID}, 1)
}

func observeBatchCompletion(mode string, seconds float64) {
	metrics.Observe("relay_batch_completion_seconds", map[string]string{
		"completion_mode": mode,
	}, seconds)
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
