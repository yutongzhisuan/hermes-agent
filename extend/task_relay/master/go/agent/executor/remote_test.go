package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/executor"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

type fakeDispatcher struct {
	spec        *pb.TaskSpec
	session     string
	results     []*pb.TaskResult
	result      *pb.TaskResult
	dispatchErr error
	resultErr   error
	calls       int
}

func (f *fakeDispatcher) DispatchTask(ctx context.Context, spec *pb.TaskSpec, masterSessionID string, allowRedispatch bool) (*pb.DispatchTaskResponse, error) {
	if f.dispatchErr != nil {
		return nil, f.dispatchErr
	}
	f.spec = spec
	f.session = masterSessionID
	return &pb.DispatchTaskResponse{TaskId: spec.GetTaskId(), Status: pb.TaskStatus_TASK_STATUS_PENDING}, nil
}

func (f *fakeDispatcher) GetTaskResult(ctx context.Context, taskID string, includeLatestCheckpoint bool) (*pb.TaskResult, error) {
	if f.resultErr != nil {
		return nil, f.resultErr
	}
	f.calls++
	if f.calls <= len(f.results) {
		return f.results[f.calls-1], nil
	}
	return f.result, nil
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
