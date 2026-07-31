package store

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

// Memory is an in-memory Store for router unit tests and local scaffolding.
type Memory struct {
	mu          sync.Mutex
	tasks       map[string]*router.Task
	batches     map[string]*router.Batch
	checkpoints map[string][]router.Checkpoint
	auditLogs   []memoryAuditRow
}

type memoryAuditRow struct {
	Action          string
	TaskID          string
	MasterSessionID string
	PayloadJSON     string
}

// NewMemory returns an empty in-memory task store.
func NewMemory() *Memory {
	return &Memory{
		tasks:       make(map[string]*router.Task),
		batches:     make(map[string]*router.Batch),
		checkpoints: make(map[string][]router.Checkpoint),
	}
}

func (m *Memory) GetTask(_ context.Context, taskID string) (*router.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return nil, nil
	}
	copy := *task
	return &copy, nil
}

func (m *Memory) InsertTask(_ context.Context, task *router.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[task.TaskID]; exists {
		return fmt.Errorf("task %s already exists", task.TaskID)
	}
	copy := *task
	m.tasks[task.TaskID] = &copy
	return nil
}

func (m *Memory) UpdateTask(_ context.Context, task *router.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[task.TaskID]; !exists {
		return fmt.Errorf("task %s not found", task.TaskID)
	}
	copy := *task
	m.tasks[task.TaskID] = &copy
	return nil
}

func (m *Memory) InsertAuditLog(_ context.Context, action, taskID, masterSessionID, payloadJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditLogs = append(m.auditLogs, memoryAuditRow{
		Action: action, TaskID: taskID, MasterSessionID: masterSessionID, PayloadJSON: payloadJSON,
	})
	return nil
}

func (m *Memory) CountAuditLogs(_ context.Context, taskID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, row := range m.auditLogs {
		if row.TaskID == taskID {
			count++
		}
	}
	return count, nil
}

func (m *Memory) ListTasks(_ context.Context, query router.ListTasksQuery) ([]*router.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	statusSet := make(map[string]struct{}, len(query.Statuses))
	for _, status := range query.Statuses {
		statusSet[status] = struct{}{}
	}
	out := make([]*router.Task, 0)
	for _, task := range m.tasks {
		if query.BatchID != "" && task.BatchID != query.BatchID {
			continue
		}
		if query.CallbackTopic != "" && task.CallbackTopic != query.CallbackTopic {
			continue
		}
		if len(statusSet) > 0 {
			if _, ok := statusSet[task.Status]; !ok {
				continue
			}
		}
		copy := *task
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) GetBatch(_ context.Context, batchID string) (*router.Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	batch, ok := m.batches[batchID]
	if !ok {
		return nil, nil
	}
	copy := *batch
	return &copy, nil
}

func (m *Memory) InsertBatch(_ context.Context, batch *router.Batch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.batches[batch.BatchID]; exists {
		return fmt.Errorf("batch %s already exists", batch.BatchID)
	}
	copy := *batch
	m.batches[batch.BatchID] = &copy
	return nil
}
