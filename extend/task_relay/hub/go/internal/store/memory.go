package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

// Memory is an in-memory Store for router unit tests and local scaffolding.
type Memory struct {
	mu    sync.Mutex
	tasks map[string]*router.Task
}

// NewMemory returns an empty in-memory task store.
func NewMemory() *Memory {
	return &Memory{tasks: make(map[string]*router.Task)}
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
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
