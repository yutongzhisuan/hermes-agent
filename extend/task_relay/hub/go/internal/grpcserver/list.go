package grpcserver

import (
	"context"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

const (
	defaultListTasksLimit = 100
	maxListTasksLimit     = 500
)

// ListTasks returns filtered task rows for Master queries.
func (s *Server) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	if req == nil {
		req = &pb.ListTasksRequest{}
	}
	limit := int(req.Limit)
	if limit <= 0 {
		limit = defaultListTasksLimit
	}
	if limit > maxListTasksLimit {
		limit = maxListTasksLimit
	}
	statuses := make([]string, 0, len(req.Statuses))
	for _, value := range req.Statuses {
		statuses = append(statuses, protoStatusToString(value))
	}
	tasks, err := s.router.ListTasks(ctx, router.ListTasksQuery{
		CallbackTopic: req.CallbackTopic,
		Statuses:      statuses,
		Limit:         limit,
	})
	if err != nil {
		return nil, routerStatusError(err)
	}
	resp := &pb.ListTasksResponse{}
	for _, task := range tasks {
		resp.Tasks = append(resp.Tasks, taskToProto(task))
	}
	return resp, nil
}

func protoStatusToString(value pb.TaskStatus) string {
	switch value {
	case pb.TaskStatus_TASK_STATUS_PENDING:
		return router.StatusPending
	case pb.TaskStatus_TASK_STATUS_RUNNING:
		return router.StatusRunning
	case pb.TaskStatus_TASK_STATUS_COMPLETED:
		return router.StatusCompleted
	case pb.TaskStatus_TASK_STATUS_FAILED:
		return router.StatusFailed
	case pb.TaskStatus_TASK_STATUS_LOST:
		return router.StatusLost
	case pb.TaskStatus_TASK_STATUS_CANCELLED:
		return router.StatusCancelled
	default:
		return ""
	}
}
