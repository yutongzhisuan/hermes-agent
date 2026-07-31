package wsserver

import "github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"

// BuildRunPayload constructs the task.run envelope for worker delivery.
func BuildRunPayload(claimed router.ClaimedTask) map[string]any {
	return map[string]any{
		"task_id":  claimed.TaskID,
		"attempt":  claimed.Attempt,
		"goal":     claimed.Goal,
		"params":   map[string]any{},
		"context":  map[string]any{},
		"toolsets": []string{},
		"run": map[string]any{
			"task_id":  claimed.TaskID,
			"attempt":  claimed.Attempt,
			"goal":     claimed.Goal,
			"params":   map[string]any{},
			"context":  map[string]any{},
			"toolsets": []string{},
		},
	}
}
