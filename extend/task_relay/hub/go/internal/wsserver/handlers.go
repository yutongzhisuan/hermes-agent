package wsserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

type announceParams struct {
	WorkerID      string         `json:"worker_id"`
	SessionModes  []string       `json:"session_modes"`
	MaxConcurrent int            `json:"max_concurrent"`
	Credit        *int           `json:"credit"`
	Toolsets      []string       `json:"toolsets"`
	Capabilities  map[string]any `json:"capabilities"`
	WakeURL       string         `json:"wake_url"`
}

type pollParams struct {
	MaxTasks int `json:"max_tasks"`
}

type completeParams struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type progressParams struct {
	TaskID  string `json:"task_id"`
	Summary string `json:"summary"`
}

type checkpointParams struct {
	TaskID       string `json:"task_id"`
	CheckpointID string `json:"checkpoint_id"`
	Summary      string `json:"summary"`
	ResumeBlob   string `json:"resume_blob"`
}

type creditParams struct {
	Available int `json:"available"`
}

type drainParams struct {
	Reason        string `json:"reason"`
	FinishRunning bool   `json:"finish_running"`
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

	s.sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
	s.announced = true
	s.modeC = supportsModeC(modes)

	if s.server.deps.Registry != nil {
		s.server.deps.Registry.Announce(context.Background(), registry.AnnounceInput{
			WorkerID:        params.WorkerID,
			SessionModes:    modes,
			MaxConcurrent:   params.MaxConcurrent,
			InitialCredit:   params.Credit,
			Toolsets:        params.Toolsets,
			Capabilities:    params.Capabilities,
			WakeURL:         params.WakeURL,
			OnlineSessionID: s.sessionID,
			Pusher:          s,
		})
	}
	if s.modeC && s.server.deps.Delivery != nil {
		s.server.deps.Delivery.OnCreditGranted(context.Background(), params.WorkerID)
	}
	return map[string]any{
		"session_id":            s.sessionID,
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
	claims := &router.WorkerClaims{
		AllowedToolsets: s.claims.AllowedToolsets,
		MaxConcurrent:   s.claims.MaxConcurrent,
	}
	claimed, err := s.server.deps.Router.ClaimForPoll(
		context.Background(),
		s.claims.WorkerID,
		maxTasks,
		claims,
	)
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return map[string]any{"offered": false}, nil
	}
	tasks := make([]map[string]any, 0, len(claimed))
	for _, item := range claimed {
		payload := BuildRunPayload(item)
		tasks = append(tasks, map[string]any{
			"claimed":     true,
			"task_id":     item.TaskID,
			"attempt":     item.Attempt,
			"claim_token": item.ClaimToken,
			"run":         payload["run"],
		})
		if task, getErr := s.server.deps.Router.GetTask(context.Background(), item.TaskID); getErr == nil {
			s.server.publishStatus(task)
		}
	}
	return map[string]any{"offered": true, "tasks": tasks}, nil
}

func (s *session) handleProgress(raw json.RawMessage) (map[string]any, error) {
	var params progressParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params")
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	if err := s.server.deps.Router.OnProgress(context.Background(), params.TaskID, params.Summary); err != nil {
		return nil, err
	}
	if task, err := s.server.deps.Router.GetTask(context.Background(), params.TaskID); err == nil {
		s.server.publishProgress(task, params.Summary)
	}
	return map[string]any{"accepted": true}, nil
}

func (s *session) handleCheckpoint(raw json.RawMessage) (map[string]any, error) {
	var params checkpointParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params")
	}
	if params.TaskID == "" || params.CheckpointID == "" {
		return nil, fmt.Errorf("task_id and checkpoint_id are required")
	}
	var blob []byte
	if params.ResumeBlob != "" {
		blob = []byte(params.ResumeBlob)
	}
	if err := s.server.deps.Router.OnCheckpoint(
		context.Background(),
		params.TaskID,
		params.CheckpointID,
		params.Summary,
		blob,
	); err != nil {
		return nil, err
	}
	return map[string]any{"checkpoint_id": params.CheckpointID}, nil
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
	if s.server.deps.Delivery != nil {
		s.server.deps.Delivery.OnCreditGranted(context.Background(), s.claims.WorkerID)
	}
	return map[string]any{
		"task_id": resp.TaskID,
		"status":  resp.Status,
		"attempt": resp.Attempt,
	}, nil
}

func (s *session) handleHeartbeat() (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	if s.server.deps.Registry != nil {
		if !s.server.deps.Registry.Heartbeat(s.claims.WorkerID, s.sessionID) {
			return nil, fmt.Errorf("heartbeat rejected for stale session")
		}
	}
	return map[string]any{"accepted": true}, nil
}

func (s *session) handleCredit(raw json.RawMessage) (map[string]any, error) {
	var params creditParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("invalid params")
		}
	}
	if s.server.deps.Registry == nil {
		return map[string]any{"accepted": 0}, nil
	}
	accepted := s.server.deps.Registry.SetCredit(s.claims.WorkerID, params.Available)
	if s.server.deps.Delivery != nil {
		s.server.deps.Delivery.OnCreditGranted(context.Background(), s.claims.WorkerID)
	}
	return map[string]any{"accepted": accepted}, nil
}

func (s *session) handleDrain(raw json.RawMessage) (map[string]any, error) {
	if s.server.deps.Registry != nil {
		s.server.deps.Registry.Drain(s.claims.WorkerID)
	}
	return map[string]any{"status": "draining"}, nil
}

// PushTaskRun implements registry.Pusher for Mode C delivery.
func (s *session) PushTaskRun(payload map[string]any) bool {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if !s.announced {
		return false
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "task.run",
		"params":  payload,
	})
	if err != nil {
		return false
	}
	return s.conn.WriteMessage(websocket.TextMessage, body) == nil
}

func supportsModeC(modes []string) bool {
	for _, mode := range modes {
		if mode == "C" || mode == "c" {
			return true
		}
	}
	return false
}

func (s *Server) publishProgress(task *router.Task, summary string) {
	if s.deps.Bus == nil || task == nil {
		return
	}
	s.deps.Bus.Publish(eventbus.Event{
		TaskID:          task.TaskID,
		BatchID:         task.BatchID,
		CallbackTopic:   task.CallbackTopic,
		Kind:            eventbus.KindProgress,
		ProgressSummary: summary,
		Status:          task.Status,
	})
}
