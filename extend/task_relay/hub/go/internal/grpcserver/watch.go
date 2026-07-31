package grpcserver

import (
	"context"
	"io"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
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
	ch, cancel, err := s.bus.Subscribe(filter, req.GetSinceEventId())
	if err != nil {
		if cursorErr, ok := err.(*eventbus.CursorOutOfRangeError); ok {
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
		return status.Errorf(codes.InvalidArgument, "%v", err)
	}
	defer cancel()

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(eventToProto(event)); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			if filter.TaskID != "" && event.Kind == eventbus.KindTerminal {
				return nil
			}
		}
	}
}

// CancelTask cancels one task or every task in a batch (batch not implemented yet).
func (s *Server) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	if req == nil || (req.TaskId == "" && req.BatchId == "") {
		return nil, status.Error(codes.InvalidArgument, "task_id or batch_id is required")
	}
	if req.BatchId != "" {
		return nil, status.Error(codes.Unimplemented, "batch cancel is not implemented in the Go hub yet")
	}

	task, err := s.router.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, routerStatusError(err)
	}
	resp := &pb.CancelTaskResponse{}
	if router.IsTerminal(task.Status) {
		resp.AlreadyTerminalTaskIds = []string{req.TaskId}
		return resp, nil
	}

	cancelResp, err := s.router.Cancel(ctx, req.TaskId, req.Reason)
	if err != nil {
		return nil, routerStatusError(err)
	}
	updated, err := s.router.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, routerStatusError(err)
	}
	switch cancelResp.Status {
	case router.StatusCancelled:
		s.publishTerminal(updated)
	case router.StatusCancelling:
		s.publishProgress(updated, "cancel requested: "+updated.Summary)
	}
	resp.CancelledTaskIds = []string{req.TaskId}
	return resp, nil
}
