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
