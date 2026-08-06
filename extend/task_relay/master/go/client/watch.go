package client

import (
	"context"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

// WatchFilter selects the WatchTask subscription scope.
type WatchFilter struct {
	Topic        string
	BatchID      string
	TaskID       string
	SinceEventID int64
}

// TerminalSnapshot accumulates TERMINAL results keyed by task_id.
type TerminalSnapshot struct {
	Results     map[string]*pb.TaskResult
	LastEventID int64
}

// CollectTerminals reads WatchTask until every taskID is terminal or ctx ends.
func CollectTerminals(
	ctx context.Context,
	stream pb.TaskRelay_WatchTaskClient,
	taskIDs []string,
) (*TerminalSnapshot, error) {
	want := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		want[id] = struct{}{}
	}

	out := &TerminalSnapshot{Results: make(map[string]*pb.TaskResult, len(taskIDs))}
	for len(out.Results) < len(want) {
		ev, err := stream.Recv()
		if err != nil {
			return out, err
		}
		if ev.EventId > out.LastEventID {
			out.LastEventID = ev.EventId
		}
		if ev.Kind != pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL {
			continue
		}
		if ev.Result == nil || ev.TaskId == "" {
			continue
		}
		if _, ok := want[ev.TaskId]; !ok {
			continue
		}
		out.Results[ev.TaskId] = ev.Result
	}
	return out, nil
}
