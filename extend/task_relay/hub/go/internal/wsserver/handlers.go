package wsserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

type announceParams struct {
	WorkerID      string   `json:"worker_id"`
	SessionModes  []string `json:"session_modes"`
	MaxConcurrent int      `json:"max_concurrent"`
}

type pollParams struct {
	MaxTasks int `json:"max_tasks"`
}

type completeParams struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

func (s *session) handleAnnounce(raw json.RawMessage) (map[string]any, error) {
	var params announceParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("invalid params")
		}
	}
	if params.WorkerID == "" {
		return nil, fmt.Errorf("worker_id is required")
	}
	if params.WorkerID != s.claims.WorkerID {
		return nil, fmt.Errorf("worker_id does not match JWT sub")
	}
	modes := params.SessionModes
	if len(modes) == 0 {
		modes = []string{"A"}
	}
	hasA := false
	for _, mode := range modes {
		if mode == "A" || mode == "a" {
			hasA = true
			break
		}
	}
	if !hasA {
		return nil, fmt.Errorf("Mode A is mandatory for all workers")
	}
	s.announced = true
	return map[string]any{
		"session_id":            fmt.Sprintf("sess-%d", time.Now().UnixNano()),
		"heartbeat_interval_ms": 30000,
		"server_time":           time.Now().UnixMilli(),
	}, nil
}

func (s *session) handlePoll(raw json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	maxTasks := 1
	if len(raw) > 0 {
		var params pollParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("invalid params")
		}
		if params.MaxTasks > 0 {
			maxTasks = params.MaxTasks
		}
	}
	claimed, err := s.server.deps.Router.ClaimForPoll(context.Background(), s.claims.WorkerID, maxTasks)
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return map[string]any{"offered": false}, nil
	}
	tasks := make([]map[string]any, 0, len(claimed))
	for _, item := range claimed {
		tasks = append(tasks, map[string]any{
			"claimed":     true,
			"task_id":     item.TaskID,
			"attempt":     item.Attempt,
			"claim_token": item.ClaimToken,
			"run": map[string]any{
				"task_id":  item.TaskID,
				"attempt":  item.Attempt,
				"goal":     item.Goal,
				"params":   map[string]any{},
				"context":  map[string]any{},
				"toolsets": []string{},
			},
		})
		if task, getErr := s.server.deps.Router.GetTask(context.Background(), item.TaskID); getErr == nil {
			s.server.publishStatus(task)
		}
	}
	return map[string]any{"offered": true, "tasks": tasks}, nil
}

func (s *session) handleComplete(raw json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	var params completeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params")
	}
	if params.TaskID == "" || params.Status == "" {
		return nil, fmt.Errorf("task_id and status are required")
	}
	status := params.Status
	if status == "completed" {
		status = router.StatusCompleted
	}
	resp, err := s.server.deps.Router.CompleteOwned(
		context.Background(),
		s.claims.WorkerID,
		params.TaskID,
		status,
		params.Summary,
	)
	if err != nil {
		return nil, err
	}
	if task, getErr := s.server.deps.Router.GetTask(context.Background(), params.TaskID); getErr == nil {
		s.server.publishTerminal(task)
	}
	return map[string]any{
		"task_id": resp.TaskID,
		"status":  resp.Status,
		"attempt": resp.Attempt,
	}, nil
}

func (s *Server) publishStatus(task *router.Task) {
	if s.deps.Bus == nil || task == nil {
		return
	}
	s.deps.Bus.Publish(eventbus.Event{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Kind:          eventbus.KindStatus,
		Status:        task.Status,
		Summary:       task.Summary,
	})
}

func (s *Server) publishTerminal(task *router.Task) {
	if s.deps.Bus == nil || task == nil {
		return
	}
	s.deps.Bus.Publish(eventbus.Event{
		TaskID:        task.TaskID,
		CallbackTopic: task.CallbackTopic,
		Kind:          eventbus.KindTerminal,
		Status:        task.Status,
		Summary:       task.Summary,
	})
}
