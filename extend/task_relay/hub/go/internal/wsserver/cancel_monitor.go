package wsserver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func (s *session) startCancelMonitor() {
	s.monitorOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.stopMonitor = cancel
		go s.cancelMonitorLoop(ctx)
	})
}

func (s *session) cancelMonitorLoop(ctx context.Context) {
	notified := make(map[string]struct{})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.announced || s.server.deps.Router == nil {
				continue
			}
			if s.server.deps.Registry != nil {
				worker := s.server.deps.Registry.Get(s.claims.WorkerID)
				if worker == nil || worker.OnlineSessionID != s.sessionID {
					return
				}
			}
			tasks, err := s.server.deps.Router.ListTasks(ctx, router.ListTasksQuery{
				WorkerID: s.claims.WorkerID,
				Statuses: []string{router.StatusCancelling},
				Limit:    100,
			})
			if err != nil {
				return
			}
			current := make(map[string]struct{}, len(tasks))
			for _, task := range tasks {
				current[task.TaskID] = struct{}{}
				if _, ok := notified[task.TaskID]; ok {
					continue
				}
				notified[task.TaskID] = struct{}{}
				if !s.pushCancel(task.TaskID, task.CancelReason, task.ClaimExpiresAt) {
					return
				}
			}
			for taskID := range notified {
				if _, ok := current[taskID]; !ok {
					delete(notified, taskID)
				}
			}
		}
	}
}

func (s *session) pushCancel(taskID, reason string, hardDeadline time.Time) bool {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if !s.announced {
		return false
	}
	if reason == "" {
		reason = "cancel requested"
	}
	params := map[string]any{
		"task_id": taskID,
		"reason":  reason,
	}
	if !hardDeadline.IsZero() {
		params["hard_deadline_at"] = float64(hardDeadline.Unix())
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "task.cancel",
		"params":  params,
	})
	if err != nil {
		return false
	}
	return s.conn.WriteMessage(websocket.TextMessage, body) == nil
}
