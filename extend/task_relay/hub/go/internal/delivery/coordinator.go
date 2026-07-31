package delivery

import (
	"context"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

// RunBuilder builds task.run payloads for worker delivery.
type RunBuilder func(claimed router.ClaimedTask) map[string]any

// Coordinator routes pending tasks to Mode C sessions (M2 subset).
type Coordinator struct {
	router  *router.Router
	registry *registry.Registry
	buildRun RunBuilder
}

// New constructs a delivery coordinator.
func New(rt *router.Router, reg *registry.Registry, build RunBuilder) *Coordinator {
	return &Coordinator{router: rt, registry: reg, buildRun: build}
}

// OnTaskPending attempts Mode C push for a newly pending task.
func (c *Coordinator) OnTaskPending(ctx context.Context, taskID string) {
	if c == nil || c.registry == nil {
		return
	}
	task, err := c.router.GetTask(ctx, taskID)
	if err != nil || task.Status != router.StatusPending {
		return
	}
	if task.TargetWorker != "" {
		c.tryPush(ctx, taskID, task.TargetWorker)
		return
	}
	for _, worker := range c.registry.List(false) {
		if c.tryPush(ctx, taskID, worker.WorkerID) {
			return
		}
	}
}

// OnCreditGranted pushes pending tasks after credit refresh.
func (c *Coordinator) OnCreditGranted(ctx context.Context, workerID string) {
	if c == nil {
		return
	}
	worker := c.registry.Get(workerID)
	if worker == nil || worker.CreditAvailable <= 0 {
		return
	}
	tasks, err := c.router.ListTasks(ctx, router.ListTasksQuery{
		Statuses: []string{router.StatusPending},
		Limit:    100,
	})
	if err != nil {
		return
	}
	for _, task := range tasks {
		if worker.CreditAvailable <= 0 {
			break
		}
		c.tryPush(ctx, task.TaskID, workerID)
		worker = c.registry.Get(workerID)
		if worker == nil || worker.CreditAvailable <= 0 {
			break
		}
	}
}

func (c *Coordinator) tryPush(ctx context.Context, taskID, workerID string) bool {
	worker := c.registry.Get(workerID)
	if worker == nil || !registry.SupportsMode(worker, "C") {
		return false
	}
	if worker.CreditAvailable <= 0 || worker.OnlineSessionID == "" {
		return false
	}
	if worker.Status == "offline" || worker.Status == "stale" || worker.Status == "draining" {
		return false
	}
	claimed, err := c.router.ClaimForWorker(ctx, taskID, workerID, nil)
	if err != nil || claimed == nil {
		return false
	}
	payload := c.buildRun(*claimed)
	if !c.registry.PushTaskRun(workerID, payload) {
		_, _ = c.router.Complete(ctx, taskID, router.StatusLost, "Mode C push delivery failed")
		return false
	}
	c.registry.ConsumeCredit(workerID)
	return true
}
