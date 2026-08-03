package registry

import (
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
)

// RouterAdapter exposes registry workers to the router package.
type RouterAdapter struct {
	*Registry
}

// NewRouterAdapter wraps a Registry for router.WorkerRegistry.
func NewRouterAdapter(reg *Registry) *RouterAdapter {
	return &RouterAdapter{Registry: reg}
}

// Get implements router.WorkerRegistry.
func (a *RouterAdapter) Get(workerID string) router.WorkerSnapshot {
	worker := a.Registry.Get(workerID)
	if worker == nil {
		return router.WorkerSnapshot{}
	}
	return router.WorkerSnapshot{
		WorkerID:      worker.WorkerID,
		Status:        worker.Status,
		SessionModes:  append([]string(nil), worker.SessionModes...),
		MaxConcurrent: worker.MaxConcurrent,
		RunningTasks:  worker.RunningTasks,
		Toolsets:      append([]string(nil), worker.Toolsets...),
		ResourcesJSON: worker.ResourcesJSON,
	}
}

// IsEligible implements router.WorkerRegistry.
func (a *RouterAdapter) IsEligible(
	worker *router.WorkerSnapshot,
	task *router.Task,
	claims *router.WorkerClaims,
) bool {
	if worker == nil || task == nil {
		return false
	}
	regWorker := &Worker{
		WorkerID:     worker.WorkerID,
		Status:       worker.Status,
		SessionModes: worker.SessionModes,
		Toolsets:     worker.Toolsets,
	}
	var regClaims *WorkerClaims
	if claims != nil {
		regClaims = &WorkerClaims{
			AllowedToolsets: claims.AllowedToolsets,
			MaxConcurrent:   claims.MaxConcurrent,
		}
	}
	return IsEligibleForPoll(
		regWorker,
		TaskView(task.TargetWorker, task.ToolsetsJSON, task.AllowedWorkerIDsJSON, task.DenyWorkerIDsJSON),
		regClaims,
	)
}

// IncRunning implements router.WorkerRegistry.
func (a *RouterAdapter) IncRunning(workerID string) {
	a.Registry.IncRunning(workerID)
}

// DecRunning implements router.WorkerRegistry.
func (a *RouterAdapter) DecRunning(workerID string) {
	a.Registry.DecRunning(workerID)
}

// ReleaseCredit implements router.WorkerRegistry.
func (a *RouterAdapter) ReleaseCredit(workerID string) {
	a.Registry.ReleaseCredit(workerID)
}
