package router

import (
	"context"
	"encoding/json"
	"time"
)

// Router implements the Hub dispatch/claim state machine for the Go port.
type Router struct {
	store   Store
	reg     WorkerRegistry
	orch    TaskOrchestrator
	cfg     RouterConfig
	now     func() time.Time
	onReady func(ctx context.Context, taskID string)
}

// WorkerRegistry is the subset of registry used by the router.
type WorkerRegistry interface {
	Get(workerID string) WorkerSnapshot
	IsEligible(worker *WorkerSnapshot, task *Task, claims *WorkerClaims) bool
	IncRunning(workerID string)
	DecRunning(workerID string)
	ReleaseCredit(workerID string)
}

// WorkerSnapshot is a minimal worker view for routing.
type WorkerSnapshot struct {
	WorkerID      string
	Status        string
	SessionModes  []string
	MaxConcurrent int
	RunningTasks  int
	Toolsets      []string
	ResourcesJSON string
}

// WorkerClaims carries JWT scope for poll claims.
type WorkerClaims struct {
	AllowedToolsets []string
	MaxConcurrent   int
}

// NewRouter constructs a Router backed by store and optional registry.
func NewRouter(store Store, reg WorkerRegistry, cfg RouterConfig) *Router {
	if cfg.TickInterval <= 0 {
		cfg = DefaultRouterConfig()
	}
	return &Router{
		store: store,
		reg:   reg,
		cfg:   cfg,
		now:   time.Now,
	}
}

// SetOrchestrator attaches the M3 batch orchestrator.
func (r *Router) SetOrchestrator(orch TaskOrchestrator) {
	r.orch = orch
}

// SetOnTaskReady registers a callback for newly ready DAG tasks.
func (r *Router) SetOnTaskReady(fn func(ctx context.Context, taskID string)) {
	r.onReady = fn
}

// SetNow overrides the clock used by the router (tests only).
func (r *Router) SetNow(fn func() time.Time) {
	if fn != nil {
		r.now = fn
	}
}

// Config returns router timeout settings.
func (r *Router) Config() RouterConfig {
	return r.cfg
}

// GetLatestCheckpoint returns the newest checkpoint for a task.
func (r *Router) GetLatestCheckpoint(ctx context.Context, taskID string) (*Checkpoint, error) {
	return r.store.GetLatestCheckpoint(ctx, taskID)
}

func encodeToolsets(toolsets []string) string {
	if len(toolsets) == 0 {
		return ""
	}
	raw, _ := json.Marshal(toolsets)
	return string(raw)
}

func encodeStringList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}
