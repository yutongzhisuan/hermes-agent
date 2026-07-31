package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/join"
)

// RelayTools wraps the framework-agnostic Hub client as Eino InvokableTools.
type RelayTools struct {
	Hub           *client.Client
	MasterSession string
}

// Build returns all Task Relay tools for ChatModelAgent.
func (r *RelayTools) Build() ([]tool.BaseTool, error) {
	if r.Hub == nil {
		return nil, fmt.Errorf("hub client is required")
	}
	dispatchTool, err := toolutils.InferTool(
		"dispatch_task",
		"Dispatch one TaskSpec to the Relay Hub",
		r.dispatchTask,
	)
	if err != nil {
		return nil, fmt.Errorf("dispatch_task: %w", err)
	}
	batchTool, err := toolutils.InferTool(
		"dispatch_batch",
		"Dispatch multiple TaskSpecs sharing one callback topic",
		r.dispatchBatch,
	)
	if err != nil {
		return nil, fmt.Errorf("dispatch_batch: %w", err)
	}
	watchTool, err := toolutils.InferTool(
		"watch_and_join",
		"Watch a callback topic and join TERMINAL results for task ids",
		r.watchAndJoin,
	)
	if err != nil {
		return nil, fmt.Errorf("watch_and_join: %w", err)
	}
	resultTool, err := toolutils.InferTool(
		"get_task_result",
		"Fetch the latest Hub result for a task id",
		r.getTaskResult,
	)
	if err != nil {
		return nil, fmt.Errorf("get_task_result: %w", err)
	}
	cancelTool, err := toolutils.InferTool(
		"cancel_task",
		"Cancel a running or pending task or batch",
		r.cancelTask,
	)
	if err != nil {
		return nil, fmt.Errorf("cancel_task: %w", err)
	}
	return []tool.BaseTool{dispatchTool, batchTool, watchTool, resultTool, cancelTool}, nil
}

func (r *RelayTools) dispatchTask(ctx context.Context, in DispatchTaskInput) (DispatchTaskOutput, error) {
	resp, err := r.Hub.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        in.TaskID,
		Goal:          in.Goal,
		CallbackTopic: in.CallbackTopic,
		TargetWorker:  in.TargetWorker,
	}, r.MasterSession, false)
	if err != nil {
		return DispatchTaskOutput{}, err
	}
	return DispatchTaskOutput{
		TaskID:        resp.GetTaskId(),
		Status:        resp.GetStatus().String(),
		CallbackTopic: resp.GetCallbackTopic(),
		IdempotentHit: resp.GetIdempotentHit(),
	}, nil
}

func (r *RelayTools) dispatchBatch(ctx context.Context, in DispatchBatchInput) (DispatchBatchOutput, error) {
	specs := make([]*pb.TaskSpec, 0, len(in.Tasks))
	for _, task := range in.Tasks {
		specs = append(specs, &pb.TaskSpec{
			TaskId:        task.TaskID,
			Goal:          task.Goal,
			CallbackTopic: in.CallbackTopic,
			TargetWorker:  task.TargetWorker,
		})
	}
	resp, err := r.Hub.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
		BatchId:         in.BatchID,
		Specs:           specs,
		MasterSessionId: r.MasterSession,
		CallbackTopic:   in.CallbackTopic,
	})
	if err != nil {
		return DispatchBatchOutput{}, err
	}
	out := DispatchBatchOutput{
		BatchID:       resp.GetBatchId(),
		CallbackTopic: resp.GetCallbackTopic(),
		Tasks:         make([]DispatchBatchTaskACK, 0, len(resp.GetTasks())),
	}
	for _, task := range resp.GetTasks() {
		out.Tasks = append(out.Tasks, DispatchBatchTaskACK{
			TaskID:        task.GetTaskId(),
			Status:        task.GetStatus().String(),
			IdempotentHit: task.GetIdempotentHit(),
		})
	}
	return out, nil
}

func (r *RelayTools) watchAndJoin(ctx context.Context, in WatchJoinInput) (WatchJoinOutput, error) {
	outcome, err := join.JoinBatch(
		ctx,
		r.Hub,
		client.WatchFilter{Topic: in.CallbackTopic},
		in.TaskIDs,
		parseJoinPolicy(in.JoinMode, in.SuccessThreshold),
	)
	if err != nil {
		return WatchJoinOutput{}, err
	}
	results := make([]TaskTerminalSummary, 0, len(outcome.Results))
	for _, taskID := range in.TaskIDs {
		result, ok := outcome.Results[taskID]
		if !ok {
			continue
		}
		results = append(results, TaskTerminalSummary{
			TaskID:  taskID,
			Status:  result.GetStatus().String(),
			Summary: result.GetSummary(),
			Error:   result.GetError(),
		})
	}
	return WatchJoinOutput{
		Satisfied:   outcome.Satisfied,
		LastEventID: outcome.LastEventID,
		Results:     results,
	}, nil
}

func (r *RelayTools) getTaskResult(ctx context.Context, in GetTaskResultInput) (GetTaskResultOutput, error) {
	result, err := r.Hub.GetTaskResult(ctx, in.TaskID, in.IncludeLatestCheckpoint)
	if err != nil {
		return GetTaskResultOutput{}, err
	}
	return GetTaskResultOutput{
		TaskID:  result.GetTaskId(),
		Status:  result.GetStatus().String(),
		Summary: result.GetSummary(),
		Error:   result.GetError(),
	}, nil
}

func (r *RelayTools) cancelTask(ctx context.Context, in CancelTaskInput) (CancelTaskOutput, error) {
	if in.TaskID == "" && in.BatchID == "" {
		return CancelTaskOutput{}, fmt.Errorf("task_id or batch_id is required")
	}
	resp, err := r.Hub.CancelTask(ctx, &pb.CancelTaskRequest{
		TaskId:  in.TaskID,
		BatchId: in.BatchID,
		Reason:  in.Reason,
	})
	if err != nil {
		return CancelTaskOutput{}, err
	}
	return CancelTaskOutput{
		CancelledTaskIDs:       resp.GetCancelledTaskIds(),
		AlreadyTerminalTaskIDs: resp.GetAlreadyTerminalTaskIds(),
	}, nil
}

func parseJoinPolicy(mode string, threshold int) join.Policy {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ANY":
		return join.Policy{Mode: join.ModeAny}
	case "MAJORITY":
		return join.Policy{Mode: join.ModeMajority}
	case "THRESHOLD":
		return join.Policy{Mode: join.ModeThreshold, SuccessThreshold: threshold}
	default:
		return join.Policy{Mode: join.ModeAll}
	}
}
