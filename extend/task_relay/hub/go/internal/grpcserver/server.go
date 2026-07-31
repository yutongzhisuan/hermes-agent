package grpcserver

import (
	"context"
	"strings"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the TaskRelay gRPC API (partial Go Hub port).
type Server struct {
	pb.UnimplementedTaskRelayServer
	router *router.Router
	bus    *eventbus.Bus
}

// New returns a gRPC server wired to the Hub router and event bus.
func New(r *router.Router, bus *eventbus.Bus) *Server {
	return &Server{router: r, bus: bus}
}

// DispatchTask handles Master task dispatch (M1).
func (s *Server) DispatchTask(ctx context.Context, req *pb.DispatchTaskRequest) (*pb.DispatchTaskResponse, error) {
	if req == nil || req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	spec := router.TaskSpec{
		TaskID:        req.Spec.TaskId,
		Goal:          req.Spec.Goal,
		CallbackTopic: req.Spec.CallbackTopic,
	}
	resp, err := s.router.DispatchTask(ctx, spec)
	if err != nil {
		return nil, routerStatusError(err)
	}
	if !resp.IdempotentHit {
		if task, getErr := s.router.GetTask(ctx, resp.TaskID); getErr == nil {
			s.publishStatus(task)
		}
	}
	return &pb.DispatchTaskResponse{
		TaskId:        resp.TaskID,
		CallbackTopic: resp.CallbackTopic,
		Status:        statusToProto(resp.Status),
		IdempotentHit: resp.IdempotentHit,
		Attempt:       int32(resp.Attempt),
	}, nil
}

// GetTaskResult returns the latest persisted task result.
func (s *Server) GetTaskResult(ctx context.Context, req *pb.TaskResultRequest) (*pb.TaskResult, error) {
	if req == nil || req.TaskId == "" {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}
	if req.IncludeLatestCheckpoint {
		return nil, status.Error(codes.Unimplemented, "checkpoints are not implemented in the Go hub yet")
	}
	task, err := s.router.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, routerStatusError(err)
	}
	return taskToProto(task), nil
}

func routerStatusError(err error) error {
	var routerErr *router.Error
	if asRouterError(err, &routerErr) {
		if strings.HasPrefix(routerErr.Msg, "task not found") {
			return status.Error(codes.NotFound, routerErr.Msg)
		}
		return status.Error(codes.InvalidArgument, routerErr.Msg)
	}
	return status.Errorf(codes.Internal, "router error: %v", err)
}

func taskToProto(task *router.Task) *pb.TaskResult {
	result := &pb.TaskResult{
		TaskId:        task.TaskID,
		Status:        statusToProto(task.Status),
		Summary:       task.Summary,
		Attempt:       int32(task.Attempt),
		MaxAttempts:   1,
		SchemaVersion: 1,
	}
	if !task.CompletedAt.IsZero() {
		result.CompletedAt = task.CompletedAt.UnixMilli()
	}
	return result
}

func asRouterError(err error, target **router.Error) bool {
	if err == nil {
		return false
	}
	if re, ok := err.(*router.Error); ok {
		*target = re
		return true
	}
	return false
}

func statusToProto(value string) pb.TaskStatus {
	switch value {
	case router.StatusPending:
		return pb.TaskStatus_TASK_STATUS_PENDING
	case router.StatusRunning:
		return pb.TaskStatus_TASK_STATUS_RUNNING
	case router.StatusCompleted:
		return pb.TaskStatus_TASK_STATUS_COMPLETED
	case router.StatusFailed:
		return pb.TaskStatus_TASK_STATUS_FAILED
	case router.StatusLost:
		return pb.TaskStatus_TASK_STATUS_LOST
	case router.StatusCancelled:
		return pb.TaskStatus_TASK_STATUS_CANCELLED
	default:
		return pb.TaskStatus_TASK_STATUS_UNSPECIFIED
	}
}
