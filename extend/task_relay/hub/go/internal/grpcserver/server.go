package grpcserver

import (
	"context"
	"strings"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/delivery"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the TaskRelay gRPC API (partial Go Hub port).
type Server struct {
	pb.UnimplementedTaskRelayServer
	router   *router.Router
	bus      *eventbus.Bus
	registry *registry.Registry
	delivery *delivery.Coordinator
	cfg      router.RouterConfig
}

// New returns a gRPC server wired to Hub runtime services.
func New(
	r *router.Router,
	bus *eventbus.Bus,
	reg *registry.Registry,
	del *delivery.Coordinator,
	cfg router.RouterConfig,
) *Server {
	return &Server{router: r, bus: bus, registry: reg, delivery: del, cfg: cfg}
}

// DispatchTask handles Master task dispatch (M1).
func (s *Server) DispatchTask(ctx context.Context, req *pb.DispatchTaskRequest) (*pb.DispatchTaskResponse, error) {
	if req == nil || req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	spec := mapTaskSpec(req.Spec)
	if err := validateContextJSON(spec.ContextJSON, s.cfg); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	resp, err := s.router.DispatchTask(ctx, spec, req.MasterSessionId, req.GetAllowRedispatch())
	if err != nil {
		return nil, routerStatusError(err)
	}
	if !resp.IdempotentHit {
		if s.delivery != nil {
			s.delivery.OnTaskPending(ctx, resp.TaskID)
		}
	}
	return dispatchResponseToProto(resp), nil
}

// GetTaskResult returns the latest persisted task result.
func (s *Server) GetTaskResult(ctx context.Context, req *pb.TaskResultRequest) (*pb.TaskResult, error) {
	if req == nil || req.TaskId == "" {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}
	task, err := s.router.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, routerStatusError(err)
	}
	result := taskToProto(task)
	if req.IncludeLatestCheckpoint {
		checkpoint, err := s.router.GetLatestCheckpoint(ctx, req.TaskId)
		if err != nil {
			return nil, routerStatusError(err)
		}
		if checkpoint != nil {
			result.LatestCheckpointId = checkpoint.CheckpointID
		}
	}
	return result, nil
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
