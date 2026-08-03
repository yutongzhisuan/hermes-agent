package wsserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/metrics"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/runpayload"
)

type announceParams struct {
	WorkerID      string         `json:"worker_id"`
	SessionModes  []string       `json:"session_modes"`
	MaxConcurrent int            `json:"max_concurrent"`
	Credit        *int           `json:"credit"`
	Toolsets      []string       `json:"toolsets"`
	Capabilities  map[string]any `json:"capabilities"`
	Resources     map[string]any `json:"resources"`
	Load          map[string]any `json:"load"`
	WakeURL       string         `json:"wake_url"`
}

type pollParams struct {
	MaxTasks           int  `json:"max_tasks"`
	MaxWaitMS          int  `json:"max_wait_ms"`
	PreferAtomicClaim  bool `json:"prefer_atomic_claim"`
}

type claimParams struct {
	TaskID     string  `json:"task_id"`
	ClaimToken string  `json:"claim_token"`
	WakeToken  string  `json:"wake_token"`
	ExpiresAt  float64 `json:"expires_at"`
}

type nackParams struct {
	TaskID     string `json:"task_id"`
	ClaimToken string `json:"claim_token"`
	Reason     string `json:"reason"`
}

type cancelAckParams struct {
	TaskID        string `json:"task_id"`
	Accepted      *bool  `json:"accepted"`
	InFlightTool  bool   `json:"in_flight_tool"`
	WillSettleBy  int64  `json:"will_settle_by"`
}

type heartbeatParams struct {
	Load      map[string]any `json:"load"`
	Resources map[string]any `json:"resources"`
}

type completeParams struct {
	TaskID     string         `json:"task_id"`
	Status     string         `json:"status"`
	Summary    string         `json:"summary"`
	ResultText string         `json:"result_text"`
	ResultJSON string         `json:"result_json"`
	Fields     map[string]any `json:"fields"`
	FieldsJSON string         `json:"fields_json"`
	Usage      map[string]any `json:"usage"`
	UsageJSON  string         `json:"usage_json"`
	Error      string         `json:"error"`
}

type progressParams struct {
	TaskID  string `json:"task_id"`
	Summary string `json:"summary"`
}

type checkpointParams struct {
	TaskID       string         `json:"task_id"`
	CheckpointID string         `json:"checkpoint_id"`
	Summary      string         `json:"summary"`
	Fields       map[string]any `json:"fields"`
	ResumeBlob   string         `json:"resume_blob"`
}

type creditParams struct {
	Available int `json:"available"`
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
	if !supportsModeA(modes) {
		return nil, fmt.Errorf("Mode A is mandatory for all workers")
	}

	s.sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
	s.announced = true
	s.modeC = supportsModeC(modes)

	if s.server.deps.Registry != nil {
		caps := params.Capabilities
		if caps == nil {
			caps = map[string]any{}
		}
		if len(params.Resources) > 0 {
			caps["resources"] = params.Resources
		}
		if len(params.Load) > 0 {
			caps["load"] = params.Load
		}
		s.server.deps.Registry.Announce(context.Background(), registry.AnnounceInput{
			WorkerID: params.WorkerID, SessionModes: modes, MaxConcurrent: params.MaxConcurrent,
			InitialCredit: params.Credit, Toolsets: params.Toolsets, Capabilities: caps,
			WakeURL: params.WakeURL, OnlineSessionID: s.sessionID, Pusher: s,
		})
	}
	s.startCancelMonitor()
	if s.modeC && s.server.deps.Delivery != nil {
		s.server.deps.Delivery.OnCreditGranted(context.Background(), params.WorkerID)
	}
	return map[string]any{
		"session_id": s.sessionID, "heartbeat_interval_ms": 30000, "server_time": time.Now().UnixMilli(),
	}, nil
}

func (s *session) handlePoll(raw json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	params := pollParams{PreferAtomicClaim: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("invalid params")
		}
	}
	maxTasks := params.MaxTasks
	if maxTasks <= 0 {
		maxTasks = 1
	}
	claims := &router.WorkerClaims{
		AllowedToolsets: s.claims.AllowedToolsets,
		MaxConcurrent:   s.claims.MaxConcurrent,
	}
	ctx := context.Background()
	if params.PreferAtomicClaim {
		return s.handleAtomicPoll(ctx, maxTasks, claims)
	}
	offered, err := s.server.deps.Router.OfferTasksForPoll(ctx, s.claims.WorkerID, maxTasks, claims)
	if err != nil {
		return nil, err
	}
	if len(offered) == 0 {
		return map[string]any{"offered": false}, nil
	}
	tasks := make([]map[string]any, 0, len(offered))
	for _, item := range offered {
		task, _ := s.server.deps.Router.GetTask(ctx, item.TaskID)
		entry := map[string]any{
			"claimed": false, "task_id": item.TaskID, "claim_token": item.ClaimToken,
			"claim_expires_at": item.ClaimExpiresAt.Unix(), "preview": runpayload.BuildPreview(task, item),
		}
		tasks = append(tasks, entry)
	}
	return map[string]any{"offered": true, "tasks": tasks}, nil
}

func (s *session) handleAtomicPoll(ctx context.Context, maxTasks int, claims *router.WorkerClaims) (map[string]any, error) {
	claimed, err := s.server.deps.Router.ClaimForPoll(ctx, s.claims.WorkerID, maxTasks, claims)
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return map[string]any{"offered": false}, nil
	}
	tasks := make([]map[string]any, 0, len(claimed))
	for _, item := range claimed {
		payload := s.server.buildRun(ctx, item)
		tasks = append(tasks, map[string]any{
			"claimed": true, "task_id": item.TaskID, "attempt": item.Attempt,
			"claim_token": item.ClaimToken, "run": payload["run"],
		})
	}
	return map[string]any{"offered": true, "tasks": tasks}, nil
}

func (s *session) handleClaim(raw json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	var params claimParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params")
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	ctx := context.Background()
	claims := &router.WorkerClaims{
		AllowedToolsets: s.claims.AllowedToolsets,
		MaxConcurrent:   s.claims.MaxConcurrent,
	}
	if params.ClaimToken != "" {
		claimed, err := s.server.deps.Router.ClaimOfferedTask(
			ctx, params.TaskID, s.claims.WorkerID, params.ClaimToken, claims,
		)
		if err != nil {
			return nil, err
		}
		if claimed == nil {
			return map[string]any{"claimed": false}, nil
		}
		payload := s.server.buildRun(ctx, *claimed)
		return map[string]any{"claimed": true, "run": payload["run"]}, nil
	}
	task, err := s.server.deps.Router.GetTask(ctx, params.TaskID)
	if err != nil || task.Status != router.StatusPending {
		return nil, fmt.Errorf("task not claimable")
	}
	if params.WakeToken != "" {
		if s.server.deps.Wake == nil || !s.server.deps.Wake.VerifyWakeToken(
			params.TaskID, s.claims.WorkerID, params.WakeToken, params.ExpiresAt,
		) {
			return nil, fmt.Errorf("invalid wake_token")
		}
	}
	claimed, err := s.server.deps.Router.ClaimForWorker(ctx, params.TaskID, s.claims.WorkerID, claims)
	if err != nil {
		return nil, err
	}
	if claimed == nil {
		return nil, fmt.Errorf("claim failed")
	}
	payload := s.server.buildRun(ctx, *claimed)
	return map[string]any{"claimed": true, "task_id": params.TaskID, "run": payload["run"]}, nil
}

func (s *session) handleNack(raw json.RawMessage) (map[string]any, error) {
	var params nackParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params")
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	released, err := s.server.deps.Router.ReleaseOffer(context.Background(), params.TaskID, params.ClaimToken)
	if err != nil {
		return nil, err
	}
	return map[string]any{"released": released}, nil
}

func (s *session) handleCancelAck(raw json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	var params cancelAckParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params")
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	response := map[string]any{"acknowledged": true}
	if params.Accepted != nil {
		response["accepted"] = *params.Accepted
	}
	if params.InFlightTool {
		response["in_flight_tool"] = true
	}
	if params.WillSettleBy != 0 {
		response["will_settle_by"] = params.WillSettleBy
	}
	return response, nil
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
		decoded, err := decodeResumeBlob(params.ResumeBlob)
		if err != nil {
			return nil, err
		}
		blob = decoded
	}
	maxBytes := s.server.deps.ResumeBlobMaxBytes
	if maxBytes <= 0 {
		maxBytes = 1_048_576
	}
	if len(blob) > maxBytes {
		return nil, fmt.Errorf("resume_blob exceeds %d bytes", maxBytes)
	}
	fieldsJSON := ""
	if params.Fields != nil {
		raw, err := json.Marshal(params.Fields)
		if err != nil {
			return nil, fmt.Errorf("invalid fields")
		}
		fieldsJSON = string(raw)
	}
	if err := s.server.deps.Router.OnCheckpoint(
		context.Background(), params.TaskID, params.CheckpointID, params.Summary, fieldsJSON, blob,
	); err != nil {
		return nil, err
	}
	metrics.Inc("relay_checkpoint_count", map[string]string{"worker_id": s.claims.WorkerID}, 1)
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
		context.Background(), s.claims.WorkerID, params.TaskID, status, params.Summary,
		router.CompleteInput{
			ResultJSON: firstNonEmpty(params.ResultText, params.ResultJSON),
			FieldsJSON: marshalOptionalJSON(params.Fields, params.FieldsJSON),
			UsageJSON:  marshalOptionalJSON(params.Usage, params.UsageJSON),
			Error:      params.Error,
		},
	)
	if err != nil {
		return nil, err
	}
	if s.server.deps.Delivery != nil {
		s.server.deps.Delivery.OnCreditGranted(context.Background(), s.claims.WorkerID)
	}
	return map[string]any{"task_id": resp.TaskID, "status": resp.Status, "attempt": resp.Attempt}, nil
}

func (s *session) handleHeartbeat(raw json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	var params heartbeatParams
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &params)
	}
	if s.server.deps.Registry != nil {
		input := &registry.HeartbeatInput{Load: params.Load, Resources: params.Resources}
		if !s.server.deps.Registry.Heartbeat(context.Background(), s.claims.WorkerID, s.sessionID, input) {
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
		s.server.deps.Registry.Drain(context.Background(), s.claims.WorkerID)
	}
	return map[string]any{"status": "draining"}, nil
}

func (s *session) handleClose(_ json.RawMessage) (map[string]any, error) {
	if !s.announced {
		return nil, fmt.Errorf("worker must announce first")
	}
	if s.server.deps.Registry != nil {
		s.server.deps.Registry.CloseSession(context.Background(), s.claims.WorkerID, s.sessionID)
	}
	s.closeAfter = true
	return map[string]any{}, nil
}

func decodeResumeBlob(raw string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return decoded, nil
	}
	return []byte(raw), nil
}

func marshalOptionalJSON(value map[string]any, raw string) string {
	if raw != "" {
		return raw
	}
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) buildRun(ctx context.Context, claimed router.ClaimedTask) map[string]any {
	return BuildRunPayload(ctx, s.deps.RunBuilder, claimed)
}

func (s *session) PushTaskRun(payload map[string]any) bool {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if !s.announced {
		return false
	}
	params := payload
	if run, ok := payload["run"].(map[string]any); ok {
		params = run
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "task.run", "params": params})
	if err != nil {
		return false
	}
	return s.conn.WriteMessage(websocket.TextMessage, body) == nil
}

func supportsModeA(modes []string) bool {
	for _, mode := range modes {
		if mode == "A" || mode == "a" {
			return true
		}
	}
	return false
}

func supportsModeC(modes []string) bool {
	for _, mode := range modes {
		if mode == "C" || mode == "c" {
			return true
		}
	}
	return false
}
