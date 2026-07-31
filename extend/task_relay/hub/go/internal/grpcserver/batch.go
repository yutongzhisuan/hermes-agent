package grpcserver

import (
	"context"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DispatchTaskBatch dispatches multiple TaskSpecs under one batch_id.
func (s *Server) DispatchTaskBatch(
	ctx context.Context,
	req *pb.DispatchTaskBatchRequest,
) (*pb.DispatchTaskBatchResponse, error) {
	if req == nil || req.BatchId == "" {
		return nil, status.Error(codes.InvalidArgument, "batch_id is required")
	}
	if len(req.Specs) == 0 {
		return nil, status.Error(codes.InvalidArgument, "specs must not be empty")
	}
	specs := make([]router.TaskSpec, 0, len(req.Specs))
	for _, spec := range req.Specs {
		if spec == nil {
			return nil, status.Error(codes.InvalidArgument, "spec entry is required")
		}
		specs = append(specs, mapTaskSpec(spec))
	}
	resp, err := s.router.DispatchTaskBatch(ctx, req.BatchId, req.CallbackTopic, mapBatchPolicyJSON(req.Policy), specs)
	if err != nil {
		return nil, routerStatusError(err)
	}
	out := &pb.DispatchTaskBatchResponse{
		BatchId:       resp.BatchID,
		CallbackTopic: resp.CallbackTopic,
		IdempotentHit: resp.IdempotentHit,
	}
	for _, task := range resp.Tasks {
		if !task.IdempotentHit {
			if row, getErr := s.router.GetTask(ctx, task.TaskID); getErr == nil {
				s.publishStatus(row)
				if s.delivery != nil {
					s.delivery.OnTaskPending(ctx, task.TaskID)
				}
			}
		}
		out.Tasks = append(out.Tasks, &pb.DispatchTaskResponse{
			TaskId:        task.TaskID,
			BatchId:       resp.BatchID,
			CallbackTopic: task.CallbackTopic,
			Status:        statusToProto(task.Status),
			IdempotentHit: task.IdempotentHit,
			Attempt:       int32(task.Attempt),
		})
	}
	return out, nil
}
