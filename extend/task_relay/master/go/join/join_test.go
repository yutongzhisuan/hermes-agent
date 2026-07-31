package join_test

import (
	"context"
	"io"
	"testing"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/join"
	"google.golang.org/grpc/metadata"
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
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !out.Satisfied || len(out.Results) != 2 {
		t.Fatalf("expected both terminals, got %+v", out)
	}
}

func TestCollectModeAnyStopsOnFirstSuccess(t *testing.T) {
	stream := &fakeWatchStream{
		events: []*pb.TaskEvent{
			terminal("t1", pb.TaskStatus_TASK_STATUS_COMPLETED, 1),
			terminal("t2", pb.TaskStatus_TASK_STATUS_COMPLETED, 2),
		},
	}
	out, err := join.Collect(context.Background(), stream, []string{"t1", "t2"}, join.Policy{Mode: join.ModeAny})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !out.Satisfied || len(out.Results) != 1 {
		t.Fatalf("expected early exit after one success, got %+v", out)
	}
}

func TestFromBatchPolicyThreshold(t *testing.T) {
	policy := join.FromBatchPolicy(&pb.BatchPolicy{
		CompletionMode:  pb.BatchPolicy_COMPLETION_MODE_THRESHOLD,
		SuccessThreshold: 2,
	})
	if policy.Mode != join.ModeThreshold || policy.SuccessThreshold != 2 {
		t.Fatalf("unexpected policy: %+v", policy)
	}
}
