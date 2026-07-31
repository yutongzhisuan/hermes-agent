package wsserver

import (
	"context"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/runpayload"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

// BuildRunPayload constructs the task.run envelope for worker delivery.
func BuildRunPayload(ctx context.Context, builder *runpayload.Builder, claimed router.ClaimedTask) map[string]any {
	if builder == nil {
		return fallbackRunPayload(claimed)
	}
	payload, err := builder.Build(ctx, claimed)
	if err != nil || payload == nil {
		return fallbackRunPayload(claimed)
	}
	return map[string]any{"run": payload}
}

func fallbackRunPayload(claimed router.ClaimedTask) map[string]any {
	run := map[string]any{
		"task_id": claimed.TaskID,
		"attempt": claimed.Attempt,
		"goal":    claimed.Goal,
	}
	return map[string]any{"run": run}
}
