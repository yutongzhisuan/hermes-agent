package grpcserver

import (
	"context"
	"io"
	"slices"

	"github.com/infa/task_relay/hub/internal/eventbus"
	"github.com/infa/task_relay/hub/internal/router"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// WatchTask streams replayed and live task events for a filter.
func (s *Server) WatchTask(req *pb.WatchTaskRequest, stream pb.TaskRelay_WatchTaskServer) error {
	filter, err := filterFromWatchRequest(req)
	if err != nil {
		return routerStatusError(err)
	}
	events, errCh, cancel, err := s.bus.Subscribe(stream.Context(), filter, req.GetSinceEventId())
	if err != nil {
		if cursorErr, ok := err.(*eventbus.CursorOutOfRangeError); ok {
			return cursorOutOfRangeStatus(cursorErr)
		}
		return status.Errorf(codes.InvalidArgument, "%v", err)
	}
	defer cancel()

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case err := <-errCh:
			if err == nil {
				continue
			}
			if slowErr, ok := err.(*eventbus.SlowConsumerError); ok {
				return slowConsumerStatus(slowErr)
			}
			return status.Errorf(codes.Internal, "watch stream: %v", err)
		case event, ok := <-events:
			if !ok {
				return nil
			}
			sendErr := sendWatchEvent(stream, errCh, eventToProto(event))
			if sendErr != nil {
				return sendErr
			}
			if filter.TaskID != "" && event.Kind == router.EventKindTerminal {
				return nil
			}
		}
	}
}

func sendWatchEvent(
	stream pb.TaskRelay_WatchTaskServer,
	errCh <-chan error,
	msg *pb.TaskEvent,
) error {
	sent := make(chan error, 1)
	go func() {
		err := stream.Send(msg)
		if err == io.EOF {
			sent <- nil
			return
		}
		sent <- err
	}()
	select {
	case err := <-errCh:
		if err == nil {
			return nil
		}
		if slowErr, ok := err.(*eventbus.SlowConsumerError); ok {
			return slowConsumerStatus(slowErr)
		}
		return status.Errorf(codes.Internal, "watch stream: %v", err)
	case err := <-sent:
		return err
	}
}

func cursorOutOfRangeStatus(cursorErr *eventbus.CursorOutOfRangeError) error {
	detail, derr := anypb.New(&pb.CursorOutOfRange{
		RequestedSinceEventId:  cursorErr.Requested,
		OldestAvailableEventId: cursorErr.Oldest,
		NewestEventId:          cursorErr.Newest,
	})
	if derr != nil {
		return status.Errorf(codes.Internal, "cursor detail: %v", derr)
	}
	st := status.New(codes.FailedPrecondition, cursorErr.Error())
	st, _ = st.WithDetails(detail)
	return st.Err()
}

func slowConsumerStatus(slowErr *eventbus.SlowConsumerError) error {
	detail, derr := anypb.New(&pb.SlowConsumer{
		DeliveredEventId: slowErr.Delivered,
		NewestEventId:    slowErr.Newest,
	})
	if derr != nil {
		return status.Errorf(codes.Internal, "slow consumer detail: %v", derr)
	}
	st := status.New(codes.ResourceExhausted, slowErr.Error())
	st, _ = st.WithDetails(detail)
	return st.Err()
}

// CancelTask cancels one task or every task in a batch.
func (s *Server) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	if req == nil || (req.TaskId == "" && req.BatchId == "") {
		return nil, status.Error(codes.InvalidArgument, "task_id or batch_id is required")
	}

	taskIDs := make([]string, 0, 4)
	if req.TaskId != "" {
		if _, err := s.router.GetTask(ctx, req.TaskId); err != nil {
			return nil, routerStatusError(err)
		}
		taskIDs = append(taskIDs, req.TaskId)
	}
	if req.BatchId != "" {
		tasks, err := s.router.ListTasks(ctx, router.ListTasksQuery{
			BatchID: req.BatchId,
			Limit:   1000,
		})
		if err != nil {
			return nil, routerStatusError(err)
		}
		if len(tasks) == 0 {
			return nil, status.Error(codes.NotFound, "batch "+req.BatchId+" not found")
		}
		for _, task := range tasks {
			if !containsString(taskIDs, task.TaskID) {
				taskIDs = append(taskIDs, task.TaskID)
			}
		}
	}

	reason := req.Reason
	if reason == "" {
		reason = "cancelled by master"
	}
	resp := &pb.CancelTaskResponse{}
	grace := graceSeconds(req.GetGraceSeconds())
	for _, taskID := range taskIDs {
		task, err := s.router.GetTask(ctx, taskID)
		if err != nil {
			return nil, routerStatusError(err)
		}
		if router.IsTerminal(task.Status) {
			resp.AlreadyTerminalTaskIds = append(resp.AlreadyTerminalTaskIds, taskID)
			continue
		}
		cancelResp, err := s.router.Cancel(ctx, taskID, reason, grace)
		if err != nil {
			return nil, routerStatusError(err)
		}
		switch cancelResp.Status {
		case router.StatusCancelled, router.StatusCancelling:
			resp.CancelledTaskIds = append(resp.CancelledTaskIds, taskID)
		}
	}
	return resp, nil
}

func graceSeconds(value int32) int {
	if value <= 0 {
		return 0
	}
	return int(value)
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}
