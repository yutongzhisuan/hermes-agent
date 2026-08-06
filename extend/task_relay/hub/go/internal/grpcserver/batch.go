package grpcserver

import (
	"context"

	"github.com/infa/task_relay/hub/internal/router"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
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
		mapped := mapTaskSpec(spec)
		if err := validateContextJSON(mapped.ContextJSON, s.cfg); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		specs = append(specs, mapped)
	}
	resp, err := s.router.DispatchTaskBatch(
		ctx, req.BatchId, req.CallbackTopic, mapBatchPolicyJSON(req.Policy), req.MasterSessionId, specs,
		req.GetAllowRedispatch(),
	)
	if err != nil {
		return nil, routerStatusError(err)
	}
	out := &pb.DispatchTaskBatchResponse{
		BatchId:       resp.BatchID,
		CallbackTopic: resp.CallbackTopic,
		IdempotentHit: resp.IdempotentHit,
	}
	for _, task := range resp.Tasks {
		if !task.IdempotentHit && s.delivery != nil {
			s.delivery.OnTaskPending(ctx, task.TaskID)
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
