package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/client"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

type pollEvent struct {
	result *pb.TaskResult
	err    error
}

type recvEvent struct {
	ev  *pb.TaskEvent
	err error
}

// fakeWatchStream implements pb.TaskRelay_WatchTaskClient over a channel.
// Recv unblocks when the watch ctx is cancelled, mirroring grpc stream behavior.
type fakeWatchStream struct {
	grpc.ClientStream
	ctx    context.Context
	recvCh chan recvEvent
}

func (s *fakeWatchStream) Recv() (*pb.TaskEvent, error) {
	select {
	case e := <-s.recvCh:
		return e.ev, e.err
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

type fakeDispatcher struct {
	mu          sync.Mutex
	spec        *pb.TaskSpec
	session     string
	events      []pollEvent
	results     []*pb.TaskResult
	result      *pb.TaskResult
	dispatchErr error
	resultErr   error
	calls       int

	// Watch is opt-in: watchErr defaults to nil but watchCh nil means the
	// fake refuses to open a stream, keeping poll-only tests deterministic.
	watchErr    error
	watchCh     chan recvEvent
	watchFilter *client.WatchFilter

	cancelErr   error
	cancelCalls int
	lastCancel  *pb.CancelTaskRequest
}

func (f *fakeDispatcher) DispatchTask(ctx context.Context, spec *pb.TaskSpec, masterSessionID string, allowRedispatch bool) (*pb.DispatchTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dispatchErr != nil {
		return nil, f.dispatchErr
	}
	f.spec = spec
	f.session = masterSessionID
	return &pb.DispatchTaskResponse{TaskId: spec.GetTaskId(), Status: pb.TaskStatus_TASK_STATUS_PENDING}, nil
}

func (f *fakeDispatcher) GetTaskResult(ctx context.Context, taskID string, includeLatestCheckpoint bool) (*pb.TaskResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) > 0 {
		ev := f.events[0]
		f.events = f.events[1:]
		return ev.result, ev.err
	}
	if f.resultErr != nil {
		return nil, f.resultErr
	}
	f.calls++
	if f.calls <= len(f.results) {
		return f.results[f.calls-1], nil
	}
	return f.result, nil
}

// Watch refuses to open unless watchCh is configured, so legacy poll-only
// tests exercise the polling path alone.
func (f *fakeDispatcher) Watch(ctx context.Context, filter client.WatchFilter) (pb.TaskRelay_WatchTaskClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watchErr != nil {
		return nil, f.watchErr
	}
	if f.watchCh == nil {
		return nil, errors.New("watch not configured")
	}
	f.watchFilter = &filter
	return &fakeWatchStream{ctx: ctx, recvCh: f.watchCh}, nil
}

func (f *fakeDispatcher) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.lastCancel = req
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	return &pb.CancelTaskResponse{}, nil
}

func (f *fakeDispatcher) cancelStats() (int, *pb.CancelTaskRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelCalls, f.lastCancel
}

func execResult(t *testing.T, status pb.TaskStatus, payload map[string]any, errMsg string) *pb.TaskResult {
	t.Helper()
	res := &pb.TaskResult{Status: status, Error: errMsg}
	if payload != nil {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		res.Fields = &pb.TaskFields{Extensions: map[string][]byte{"exec": raw}}
	}
	return res
}

func execPayload(exitCode int, stdout, stderr string) map[string]any {
	return map[string]any{
		"exit_code": exitCode,
		"stdout":    stdout,
		"stderr":    stderr,
		"timed_out": false,
		"canceled":  false,
	}
}

func TestRemoteSuccess(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "hi", ""), "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "echo hi", WorkDir: "/tmp", Timeout: 30 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "hi", res.Stdout)
	require.Equal(t, "remote", res.Backend)
	require.False(t, res.StartedAt.IsZero())
	require.False(t, res.FinishedAt.IsZero())

	require.NotNil(t, fake.spec)
	require.Equal(t, "echo hi", fake.spec.GetParams()["cmd"])
	require.Equal(t, "/tmp", fake.spec.GetParams()["workdir"])
	require.Equal(t, "30", fake.spec.GetParams()["timeout_seconds"])
	require.Equal(t, []string{"shell"}, fake.spec.GetToolsets())
	require.Equal(t, int32(30), fake.spec.GetTimeoutSeconds())
	require.Equal(t, "session-1", fake.session)
}

func TestRemoteRoundsUpSubSecondTimeout(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "", ""), "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 1500 * time.Millisecond})
	require.NoError(t, err)
	require.Equal(t, "2", fake.spec.GetParams()["timeout_seconds"])
	require.Equal(t, int32(2), fake.spec.GetTimeoutSeconds())
}

func TestRemoteSuccessEmptyWorkdir(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "", ""), "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "pwd", Timeout: 5 * time.Second})
	require.NoError(t, err)
	_, hasWorkdir := fake.spec.GetParams()["workdir"]
	require.False(t, hasWorkdir)
}

func TestRemoteDispatchError(t *testing.T) {
	fake := &fakeDispatcher{dispatchErr: errors.New("hub unreachable")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "hub unreachable")
}

func TestRemoteNonZeroExit(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(3, "", "boom"), "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "exit 3", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 3, res.ExitCode)
	require.Equal(t, "boom", res.Stderr)
}

func TestRemoteMalformedExtensions(t *testing.T) {
	fake := &fakeDispatcher{result: &pb.TaskResult{
		Status: pb.TaskStatus_TASK_STATUS_COMPLETED,
		Fields: &pb.TaskFields{Extensions: map[string][]byte{"exec": []byte("not-json")}},
	}}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "malformed")
}

func TestRemoteMissingExtensions(t *testing.T) {
	fake := &fakeDispatcher{result: &pb.TaskResult{Status: pb.TaskStatus_TASK_STATUS_COMPLETED}}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "malformed")
}

func TestRemoteLost(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_LOST, nil, "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "lost")
}

func TestRemoteFailedNoPayload(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_FAILED, nil, "worker exploded")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "worker exploded")
}

func TestRemoteFailedWithPayloadForcesExitCode(t *testing.T) {
	tests := []struct {
		name         string
		status       pb.TaskStatus
		payload      map[string]any
		wantCanceled bool
		wantTimedOut bool
	}{
		{
			name:   "failed with successful-looking payload",
			status: pb.TaskStatus_TASK_STATUS_FAILED,
			payload: map[string]any{
				"exit_code": 0, "stdout": "partial out", "stderr": "partial err",
				"timed_out": false, "canceled": false,
			},
		},
		{
			name:   "failed with canceled payload",
			status: pb.TaskStatus_TASK_STATUS_FAILED,
			payload: map[string]any{
				"exit_code": 0, "stdout": "partial out", "stderr": "",
				"timed_out": false, "canceled": true,
			},
		},
		{
			name:   "failed keeps payload timed_out",
			status: pb.TaskStatus_TASK_STATUS_FAILED,
			payload: map[string]any{
				"exit_code": 0, "stdout": "", "stderr": "",
				"timed_out": true, "canceled": false,
			},
			wantTimedOut: true,
		},
		{
			name:   "cancelled forces canceled regardless of payload",
			status: pb.TaskStatus_TASK_STATUS_CANCELLED,
			payload: map[string]any{
				"exit_code": 0, "stdout": "partial out", "stderr": "",
				"timed_out": false, "canceled": false,
			},
			wantCanceled: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDispatcher{result: execResult(t, tc.status, tc.payload, "")}
			backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
			res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
			require.NoError(t, err)
			require.Equal(t, -1, res.ExitCode)
			require.Equal(t, tc.payload["stdout"], res.Stdout)
			require.Equal(t, tc.payload["stderr"], res.Stderr)
			require.Equal(t, tc.wantCanceled, res.Canceled)
			require.Equal(t, tc.wantTimedOut, res.TimedOut)
		})
	}
}

func TestRemoteContextTimeout(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_RUNNING, nil, "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := backend.Run(ctx, executor.Spec{Command: "sleep 99", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.True(t, res.TimedOut)
	require.False(t, res.Canceled)
	require.Equal(t, "remote", res.Backend)
	require.False(t, res.StartedAt.IsZero())
	require.False(t, res.FinishedAt.IsZero())
}

func TestRemoteContextCanceled(t *testing.T) {
	fake := &fakeDispatcher{result: execResult(t, pb.TaskStatus_TASK_STATUS_RUNNING, nil, "")}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res, err := backend.Run(ctx, executor.Spec{Command: "sleep 99", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.False(t, res.TimedOut)
	require.True(t, res.Canceled)
}

func TestRemotePollsUntilTerminal(t *testing.T) {
	fake := &fakeDispatcher{
		results: []*pb.TaskResult{
			{Status: pb.TaskStatus_TASK_STATUS_PENDING},
			{Status: pb.TaskStatus_TASK_STATUS_RUNNING},
		},
		result: execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "done", ""), ""),
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "done", res.Stdout)
	require.Equal(t, 3, fake.calls)
}

func TestRemoteToleratesTransientPollErrors(t *testing.T) {
	completed := execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "done", ""), "")
	fake := &fakeDispatcher{
		events: []pollEvent{
			{err: errors.New("hub blip")},
			{err: errors.New("hub blip")},
			{result: completed},
		},
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "done", res.Stdout)
	require.Empty(t, fake.events)
}

func TestRemotePollErrorCounterResetsOnSuccess(t *testing.T) {
	completed := execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "done", ""), "")
	fake := &fakeDispatcher{
		events: []pollEvent{
			{err: errors.New("hub blip")},
			{err: errors.New("hub blip")},
			{result: &pb.TaskResult{Status: pb.TaskStatus_TASK_STATUS_RUNNING}},
			{err: errors.New("hub blip")},
			{err: errors.New("hub blip")},
			{result: completed},
		},
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Empty(t, fake.events)
}

func TestRemoteAbortsAfterThreeConsecutivePollErrors(t *testing.T) {
	completed := execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "done", ""), "")
	fake := &fakeDispatcher{
		events: []pollEvent{
			{err: errors.New("hub down")},
			{err: errors.New("hub down")},
			{err: errors.New("hub down")},
			{result: completed},
		},
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	_, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "get task result")
	require.ErrorContains(t, err, "hub down")
	// Run must give up on the third consecutive failure, never reaching the
	// scripted success behind it.
	require.Len(t, fake.events, 1)
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), baseline, "producer goroutines leaked")
}

func TestRemoteWatchWins(t *testing.T) {
	running := &pb.TaskResult{Status: pb.TaskStatus_TASK_STATUS_RUNNING}
	completed := execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "watched", ""), "")
	watchCh := make(chan recvEvent, 1)
	fake := &fakeDispatcher{
		results: []*pb.TaskResult{running, completed},
		result:  completed,
		watchCh: watchCh,
	}
	baseline := runtime.NumGoroutine()
	backend := executor.NewRemoteBackend(fake, "session-1", time.Hour)

	started := time.Now()
	done := make(chan executor.JobResult, 1)
	go func() {
		res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
		require.NoError(t, err)
		done <- res
	}()

	// First GetTaskResult is the watch snapshot (RUNNING); the terminal event
	// then triggers a fetch that returns COMPLETED.
	require.Eventually(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.calls >= 1
	}, 2*time.Second, time.Millisecond)
	fake.mu.Lock()
	taskID := fake.spec.GetTaskId()
	fake.mu.Unlock()
	watchCh <- recvEvent{ev: &pb.TaskEvent{Kind: pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL, TaskId: taskID}}

	var res executor.JobResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch-driven completion did not win over slow polling")
	}
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "watched", res.Stdout)

	fake.mu.Lock()
	filter := fake.watchFilter
	fake.mu.Unlock()
	require.NotNil(t, filter)
	require.Equal(t, taskID, filter.TaskID)

	waitForGoroutines(t, baseline)
}

func TestRemotePollWins(t *testing.T) {
	fake := &fakeDispatcher{
		watchErr: errors.New("watch unsupported"),
		result:   execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "polled", ""), ""),
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "polled", res.Stdout)
}

func TestRemoteWatchStreamDiesPollContinues(t *testing.T) {
	watchCh := make(chan recvEvent, 1)
	watchCh <- recvEvent{err: errors.New("stream reset")}
	fake := &fakeDispatcher{
		watchCh: watchCh,
		result:  execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "polled", ""), ""),
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "polled", res.Stdout)
}

func TestRemoteWatchSnapshotRace(t *testing.T) {
	// Result is already terminal before the watch subscription opens; the
	// snapshot closes the dispatch-to-subscribe race without waiting for events.
	watchCh := make(chan recvEvent)
	fake := &fakeDispatcher{
		watchCh: watchCh,
		result:  execResult(t, pb.TaskStatus_TASK_STATUS_COMPLETED, execPayload(0, "snapshot", ""), ""),
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Hour)
	started := time.Now()
	res, err := backend.Run(context.Background(), executor.Spec{Command: "true", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "snapshot", res.Stdout)
}

func TestRemoteCancelPropagated(t *testing.T) {
	watchCh := make(chan recvEvent)
	fake := &fakeDispatcher{
		watchCh: watchCh,
		result:  &pb.TaskResult{Status: pb.TaskStatus_TASK_STATUS_RUNNING},
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res, err := backend.Run(ctx, executor.Spec{Command: "sleep 99", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.True(t, res.Canceled)
	require.False(t, res.TimedOut)

	cancelCalls, lastCancel := fake.cancelStats()
	require.Equal(t, 1, cancelCalls)
	require.NotNil(t, lastCancel)
	fake.mu.Lock()
	taskID := fake.spec.GetTaskId()
	fake.mu.Unlock()
	require.Equal(t, taskID, lastCancel.GetTaskId())
}

func TestRemoteCancelBestEffort(t *testing.T) {
	fake := &fakeDispatcher{
		cancelErr: errors.New("hub unreachable"),
		result:    &pb.TaskResult{Status: pb.TaskStatus_TASK_STATUS_RUNNING},
	}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res, err := backend.Run(ctx, executor.Spec{Command: "sleep 99", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.True(t, res.Canceled)

	cancelCalls, _ := fake.cancelStats()
	require.Equal(t, 1, cancelCalls)
}

func TestRemoteTimeoutPropagatesCancel(t *testing.T) {
	fake := &fakeDispatcher{result: &pb.TaskResult{Status: pb.TaskStatus_TASK_STATUS_RUNNING}}
	backend := executor.NewRemoteBackend(fake, "session-1", time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := backend.Run(ctx, executor.Spec{Command: "sleep 99", Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.True(t, res.TimedOut)
	require.False(t, res.Canceled)

	cancelCalls, lastCancel := fake.cancelStats()
	require.Equal(t, 1, cancelCalls)
	require.NotNil(t, lastCancel)
}
