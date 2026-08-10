package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

// TaskDispatcher is the slice of the master hub client that RemoteBackend needs.
type TaskDispatcher interface {
	DispatchTask(ctx context.Context, spec *pb.TaskSpec, masterSessionID string, allowRedispatch bool) (*pb.DispatchTaskResponse, error)
	GetTaskResult(ctx context.Context, taskID string, includeLatestCheckpoint bool) (*pb.TaskResult, error)
}

type remoteBackend struct {
	dispatcher   TaskDispatcher
	session      string
	pollInterval time.Duration
}

// NewRemoteBackend builds the hub-dispatched executor backend.
// pollInterval <= 0 defaults to 500ms.
func NewRemoteBackend(dispatcher TaskDispatcher, session string, pollInterval time.Duration) Executor {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	return &remoteBackend{dispatcher: dispatcher, session: session, pollInterval: pollInterval}
}

func (r *remoteBackend) Name() string { return "remote" }

// Sandboxed reports false: worker-side sandboxing is the worker environment's
// concern and the master cannot observe it from here.
func (r *remoteBackend) Sandboxed() bool { return false }

type remoteExecPayload struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
	Canceled bool   `json:"canceled"`
}

func (r *remoteBackend) Run(ctx context.Context, spec Spec) (JobResult, error) {
	res := JobResult{Backend: r.Name(), StartedAt: time.Now()}

	params := map[string]string{
		"cmd":             spec.Command,
		"timeout_seconds": strconv.Itoa(int(spec.Timeout.Seconds())),
	}
	if spec.WorkDir != "" {
		params["workdir"] = spec.WorkDir
	}
	taskSpec := &pb.TaskSpec{
		TaskId:         uuid.NewString(),
		Goal:           "remote shell exec",
		Params:         params,
		Toolsets:       []string{"shell"},
		TimeoutSeconds: int32(spec.Timeout.Seconds()),
	}

	if _, err := r.dispatcher.DispatchTask(ctx, taskSpec, r.session, false); err != nil {
		res.FinishedAt = time.Now()
		return res, fmt.Errorf("dispatch: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		result, err := r.dispatcher.GetTaskResult(ctx, taskSpec.GetTaskId(), false)
		if err != nil && ctx.Err() == nil {
			res.FinishedAt = time.Now()
			return res, fmt.Errorf("get task result: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return abandon(res, ctxErr)
		}
		if err != nil {
			continue
		}
		switch result.GetStatus() {
		case pb.TaskStatus_TASK_STATUS_COMPLETED, pb.TaskStatus_TASK_STATUS_FAILED,
			pb.TaskStatus_TASK_STATUS_LOST, pb.TaskStatus_TASK_STATUS_CANCELLED:
			res.FinishedAt = time.Now()
			return r.finalize(res, result)
		}
		select {
		case <-ctx.Done():
			return abandon(res, ctx.Err())
		case <-ticker.C:
		}
	}
}

// abandon mirrors the local backend timeout/cancel convention: return a
// JobResult without a Go error. The remote task is NOT cancelled on the hub
// (round-1 scope); it terminates on the worker side by its own timeout.
func abandon(res JobResult, ctxErr error) (JobResult, error) {
	res.FinishedAt = time.Now()
	res.ExitCode = -1
	if errors.Is(ctxErr, context.Canceled) {
		res.Canceled = true
	} else {
		res.TimedOut = true
	}
	return res, nil
}

func (r *remoteBackend) finalize(res JobResult, result *pb.TaskResult) (JobResult, error) {
	status := result.GetStatus()
	if status == pb.TaskStatus_TASK_STATUS_LOST {
		return res, fmt.Errorf("remote task lost: %s", result.GetError())
	}
	payload, err := parseExecPayload(result)
	if err != nil {
		if status == pb.TaskStatus_TASK_STATUS_FAILED {
			return res, fmt.Errorf("remote task failed: %s", result.GetError())
		}
		return res, err
	}
	res.ExitCode = payload.ExitCode
	// Worker caps each stream at 1 MiB; master Spec has no output limit field,
	// so no MaxOutputBytes truncation is applied here.
	res.Stdout = payload.Stdout
	res.Stderr = payload.Stderr
	res.TimedOut = payload.TimedOut
	res.Canceled = payload.Canceled
	if status == pb.TaskStatus_TASK_STATUS_FAILED || status == pb.TaskStatus_TASK_STATUS_CANCELLED {
		res.ExitCode = -1
	}
	return res, nil
}

func parseExecPayload(result *pb.TaskResult) (remoteExecPayload, error) {
	var payload remoteExecPayload
	raw, ok := result.GetFields().GetExtensions()["exec"]
	if !ok || len(raw) == 0 {
		return payload, fmt.Errorf("remote exec result malformed: missing extensions[exec]")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, fmt.Errorf("remote exec result malformed: %w", err)
	}
	return payload, nil
}
