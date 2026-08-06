package join_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/infa/task_relay/master/join"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

type fakeWatchStream struct {
	events []*pb.TaskEvent
	idx    int
}

func (f *fakeWatchStream) Recv() (*pb.TaskEvent, error) {
	if f.idx >= len(f.events) {
		return nil, io.EOF
	}
	ev := f.events[f.idx]
	f.idx++
	return ev, nil
}

func (f *fakeWatchStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeWatchStream) Trailer() metadata.MD         { return nil }
func (f *fakeWatchStream) CloseSend() error             { return nil }
func (f *fakeWatchStream) Context() context.Context     { return context.Background() }
func (f *fakeWatchStream) SendMsg(any) error            { return nil }
func (f *fakeWatchStream) RecvMsg(any) error            { return nil }

func terminal(taskID string, status pb.TaskStatus, eventID int64) *pb.TaskEvent {
	return &pb.TaskEvent{
		EventId: eventID,
		TaskId:  taskID,
		Kind:    pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL,
		Result:  &pb.TaskResult{TaskId: taskID, Status: status},
	}
}

func TestCollectModeAllWaitsForEveryTerminal(t *testing.T) {
	stream := &fakeWatchStream{
		events: []*pb.TaskEvent{
			terminal("t1", pb.TaskStatus_TASK_STATUS_COMPLETED, 1),
			terminal("t2", pb.TaskStatus_TASK_STATUS_FAILED, 2),
		},
	}
	out, err := join.Collect(context.Background(), stream, []string{"t1", "t2"}, join.Policy{Mode: join.ModeAll})
	require.NoError(t, err)
	require.True(t, out.Satisfied)
	require.Len(t, out.Results, 2)
}

func TestCollectModeAnyStopsOnFirstSuccess(t *testing.T) {
	stream := &fakeWatchStream{
		events: []*pb.TaskEvent{
			terminal("t1", pb.TaskStatus_TASK_STATUS_COMPLETED, 1),
			terminal("t2", pb.TaskStatus_TASK_STATUS_COMPLETED, 2),
		},
	}
	out, err := join.Collect(context.Background(), stream, []string{"t1", "t2"}, join.Policy{Mode: join.ModeAny})
	require.NoError(t, err)
	require.True(t, out.Satisfied)
	require.Len(t, out.Results, 1)
}

func TestFromBatchPolicyThreshold(t *testing.T) {
	policy := join.FromBatchPolicy(&pb.BatchPolicy{
		CompletionMode:   pb.BatchPolicy_COMPLETION_MODE_THRESHOLD,
		SuccessThreshold: 2,
	})
	require.Equal(t, join.ModeThreshold, policy.Mode)
	require.Equal(t, 2, policy.SuccessThreshold)
}
