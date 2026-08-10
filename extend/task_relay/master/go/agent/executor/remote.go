package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/infa/task_relay/master/client"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

// TaskDispatcher is the slice of the master hub client that RemoteBackend needs.
type TaskDispatcher interface {
	DispatchTask(ctx context.Context, spec *pb.TaskSpec, masterSessionID string, allowRedispatch bool) (*pb.DispatchTaskResponse, error)
	GetTaskResult(ctx context.Context, taskID string, includeLatestCheckpoint bool) (*pb.TaskResult, error)
	Watch(ctx context.Context, filter client.WatchFilter) (pb.TaskRelay_WatchTaskClient, error)
	CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error)
}

var _ TaskDispatcher = (*client.Client)(nil)

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

// maxConsecutivePollErrors bounds how many consecutive GetTaskResult failures
// are tolerated before abandoning the poll loop. Transient hub/network errors
// must not abandon an in-flight remote task that the worker is still running.
const maxConsecutivePollErrors = 3

// outcome is delivered by whichever producer (watch or poll) observes the
// terminal state first. err is set only by the poll producer after
// maxConsecutivePollErrors consecutive failures.
type outcome struct {
	res *pb.TaskResult
	err error
}

func isTerminal(status pb.TaskStatus) bool {
	switch status {
	case pb.TaskStatus_TASK_STATUS_COMPLETED, pb.TaskStatus_TASK_STATUS_FAILED,
		pb.TaskStatus_TASK_STATUS_LOST, pb.TaskStatus_TASK_STATUS_CANCELLED:
		return true
	}
	return false
}

func (r *remoteBackend) Run(ctx context.Context, spec Spec) (JobResult, error) {
	res := JobResult{Backend: r.Name(), StartedAt: time.Now()}

	// Round up so sub-second timeouts never collapse to zero on the wire.
	timeoutSeconds := int(math.Ceil(spec.Timeout.Seconds()))
	params := map[string]string{
		"cmd":             spec.Command,
		"timeout_seconds": strconv.Itoa(timeoutSeconds),
	}
	if spec.WorkDir != "" {
		params["workdir"] = spec.WorkDir
	}
	taskSpec := &pb.TaskSpec{
		TaskId:         uuid.NewString(),
		Goal:           "remote shell exec",
		Params:         params,
		Toolsets:       []string{"shell"},
		TimeoutSeconds: int32(timeoutSeconds),
	}

	if _, err := r.dispatcher.DispatchTask(ctx, taskSpec, r.session, false); err != nil {
		res.FinishedAt = time.Now()
		return res, fmt.Errorf("dispatch: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	// runCtx gates the producer goroutines; cancelling it on return unblocks
	// the losing producer (grpc stream Recv honors ctx cancel, the poll loop
	// selects on ctx) so no goroutine leaks past Run.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	// Watch and poll run concurrently; whichever observes the terminal state
	// first wins. Capacity 2 lets both producers deliver without blocking.
	resultCh := make(chan outcome, 2)
	go r.watchLoop(runCtx, taskSpec.GetTaskId(), resultCh)
	go r.pollLoop(runCtx, taskSpec.GetTaskId(), resultCh)

	select {
	case oc := <-resultCh:
		res.FinishedAt = time.Now()
		if oc.err != nil {
			return res, oc.err
		}
		return r.finalize(res, oc.res)
	case <-ctx.Done():
		r.cancelRemote(taskSpec.GetTaskId())
		return abandon(res, ctx.Err())
	}
}

// watchLoop is the accelerator producer: it delivers as soon as the hub
// reports the task terminal. Any failure (open or Recv) silently returns —
// the poll producer guarantees progress regardless.
func (r *remoteBackend) watchLoop(ctx context.Context, taskID string, resultCh chan<- outcome) {
	stream, err := r.dispatcher.Watch(ctx, client.WatchFilter{TaskID: taskID})
	if err != nil {
		return
	}
	// Snapshot once to close the dispatch-to-subscribe race: the task may
	// already be terminal before the watch stream sees any event.
	if result, err := r.dispatcher.GetTaskResult(ctx, taskID, false); err == nil && isTerminal(result.GetStatus()) {
		resultCh <- outcome{res: result}
		return
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			return
		}
		if ev.GetKind() != pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL || ev.GetTaskId() != taskID {
			continue
		}
		// Fetch the full result; the event itself only signals terminality.
		if result, err := r.dispatcher.GetTaskResult(ctx, taskID, false); err == nil {
			resultCh <- outcome{res: result}
		}
		return
	}
}

// pollLoop is the safety-net producer: it always makes progress even when the
// watch stream never opens or dies mid-task.
func (r *remoteBackend) pollLoop(ctx context.Context, taskID string, resultCh chan<- outcome) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	consecutivePollErrors := 0
	for {
		result, err := r.dispatcher.GetTaskResult(ctx, taskID, false)
		if err != nil && ctx.Err() == nil {
			consecutivePollErrors++
			if consecutivePollErrors >= maxConsecutivePollErrors {
				resultCh <- outcome{err: fmt.Errorf("get task result: %w", err)}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		consecutivePollErrors = 0
		if ctx.Err() != nil {
			return
		}
		if isTerminal(result.GetStatus()) {
			resultCh <- outcome{res: result}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cancelRemote best-effort propagates local ctx termination to the remote
// task. Fire-and-forget: we do not wait for the task to reach CANCELLED.
func (r *remoteBackend) cancelRemote(taskID string) {
	// The caller's ctx is already dead; a fresh bounded context gives the
	// cancel RPC itself a chance to complete.
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if _, err := r.dispatcher.CancelTask(cctx, &pb.CancelTaskRequest{
		TaskId: taskID,
		Reason: "master executor context done",
	}); err != nil {
		slog.Warn("remote task cancel failed", "task_id", taskID, "error", err)
	}
}

// abandon mirrors the local backend timeout/cancel convention: return a
// JobResult without a Go error. The remote task has already been asked to
// cancel best-effort via cancelRemote before abandon is called.
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
		// Non-COMPLETED terminal states do not trust the payload exit code; the
		// job-level Canceled flag comes from the task status, not the payload.
		res.ExitCode = -1
		res.Canceled = status == pb.TaskStatus_TASK_STATUS_CANCELLED
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
