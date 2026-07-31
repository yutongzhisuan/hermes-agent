// Package join implements Master-primary batch join over WatchTask streams.
package join

import (
	"context"
	"fmt"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
)

// Mode selects when a batch join stops reading the Watch stream.
type Mode int

const (
	ModeAll Mode = iota
	ModeAny
	ModeMajority
	ModeThreshold
)

// Policy controls early exit while collecting TERMINAL events.
type Policy struct {
	Mode             Mode
	SuccessThreshold int
}

// Outcome holds collected terminal results and the last seen event id.
type Outcome struct {
	Results     map[string]*pb.TaskResult
	LastEventID int64
	Satisfied   bool
}

// FromBatchPolicy maps Hub BatchPolicy protobuf to a join Policy.
func FromBatchPolicy(p *pb.BatchPolicy) Policy {
	if p == nil {
		return Policy{Mode: ModeAll}
	}
	switch p.GetCompletionMode() {
	case pb.BatchPolicy_COMPLETION_MODE_ANY:
		return Policy{Mode: ModeAny}
	case pb.BatchPolicy_COMPLETION_MODE_MAJORITY:
		return Policy{Mode: ModeMajority}
	case pb.BatchPolicy_COMPLETION_MODE_THRESHOLD:
		threshold := int(p.GetSuccessThreshold())
		if threshold < 1 {
			threshold = 1
		}
		return Policy{Mode: ModeThreshold, SuccessThreshold: threshold}
	default:
		return Policy{Mode: ModeAll}
	}
}

// JoinBatch dispatches nothing; it watches a topic/batch and joins task terminals.
func JoinBatch(
	ctx context.Context,
	hub *client.Client,
	filter client.WatchFilter,
	taskIDs []string,
	policy Policy,
) (*Outcome, error) {
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("task_ids are required")
	}
	stream, err := hub.Watch(ctx, filter)
	if err != nil {
		return nil, err
	}
	return Collect(ctx, stream, taskIDs, policy)
}

// Collect reads TERMINAL events until the join policy is satisfied.
func Collect(
	ctx context.Context,
	stream pb.TaskRelay_WatchTaskClient,
	taskIDs []string,
	policy Policy,
) (*Outcome, error) {
	want := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		if id == "" {
			return nil, fmt.Errorf("task_id must not be empty")
		}
		want[id] = struct{}{}
	}

	out := &Outcome{Results: make(map[string]*pb.TaskResult, len(taskIDs))}
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		ev, err := stream.Recv()
		if err != nil {
			return out, err
		}
		if ev.GetEventId() > out.LastEventID {
			out.LastEventID = ev.GetEventId()
		}
		if ev.GetKind() != pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL {
			continue
		}
		taskID := ev.GetTaskId()
		if taskID == "" || ev.GetResult() == nil {
			continue
		}
		if _, ok := want[taskID]; !ok {
			continue
		}
		out.Results[taskID] = ev.GetResult()
		if policySatisfied(out.Results, len(taskIDs), policy) {
			out.Satisfied = true
			return out, nil
		}
	}
}

func policySatisfied(results map[string]*pb.TaskResult, total int, policy Policy) bool {
	switch policy.Mode {
	case ModeAny:
		return countCompleted(results) >= 1
	case ModeMajority:
		return countCompleted(results) > total/2
	case ModeThreshold:
		need := policy.SuccessThreshold
		if need < 1 {
			need = 1
		}
		return countCompleted(results) >= need
	default:
		return len(results) >= total
	}
}

func countCompleted(results map[string]*pb.TaskResult) int {
	n := 0
	for _, result := range results {
		if result.GetStatus() == pb.TaskStatus_TASK_STATUS_COMPLETED {
			n++
		}
	}
	return n
}
