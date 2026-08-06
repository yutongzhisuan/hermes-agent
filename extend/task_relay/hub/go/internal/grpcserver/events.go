package grpcserver

import (
	"encoding/json"

	"github.com/infa/task_relay/hub/internal/eventbus"
	"github.com/infa/task_relay/hub/internal/router"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

func eventToProto(event *router.TaskEvent) *pb.TaskEvent {
	if event == nil {
		return &pb.TaskEvent{}
	}
	proto := &pb.TaskEvent{
		EventId: event.EventID,
		EventAt: event.EventAt.UnixMilli(),
		TaskId:  event.TaskID,
		BatchId: event.BatchID,
		Kind:    kindToProto(event.Kind),
	}
	payload := map[string]any{}
	if event.PayloadJSON != "" {
		_ = json.Unmarshal([]byte(event.PayloadJSON), &payload)
	}
	applyTraceContext(proto, payload)
	switch event.Kind {
	case router.EventKindTerminal:
		applyTerminalPayload(proto, event, payload)
	case router.EventKindProgress:
		proto.ProgressSummary = stringValue(payload["summary"])
		proto.Result = &pb.TaskResult{Attempt: int32(numberValue(payload["attempt"]))}
	case router.EventKindStatus:
		proto.Result = &pb.TaskResult{
			TaskId:  event.TaskID,
			Status:  statusToProto(stringValue(payload["status"])),
			Attempt: int32(numberValue(payload["attempt"])),
		}
	case router.EventKindCheckpoint:
		proto.Checkpoint = &pb.TaskCheckpoint{
			TaskId:       event.TaskID,
			CheckpointId: stringValue(payload["checkpoint_id"]),
			EventId:      event.EventID,
			CheckpointAt: event.EventAt.UnixMilli(),
			Summary:      stringValue(payload["summary"]),
		}
	case router.EventKindAggregate:
		proto.Aggregate = aggregateToProto(payload)
	}
	return proto
}

func kindToProto(kind string) pb.TaskEventKind {
	switch kind {
	case router.EventKindStatus:
		return pb.TaskEventKind_TASK_EVENT_KIND_STATUS
	case router.EventKindProgress:
		return pb.TaskEventKind_TASK_EVENT_KIND_PROGRESS
	case router.EventKindTerminal:
		return pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL
	case router.EventKindCheckpoint:
		return pb.TaskEventKind_TASK_EVENT_KIND_CHECKPOINT
	case router.EventKindAggregate:
		return pb.TaskEventKind_TASK_EVENT_KIND_AGGREGATE
	default:
		return pb.TaskEventKind_TASK_EVENT_KIND_UNSPECIFIED
	}
}

func applyTerminalPayload(proto *pb.TaskEvent, event *router.TaskEvent, payload map[string]any) {
	proto.Result = &pb.TaskResult{
		TaskId:        event.TaskID,
		Status:        statusToProto(stringValue(payload["status"])),
		Summary:       stringValue(payload["summary"]),
		Error:         stringValue(payload["error"]),
		Attempt:       int32(numberValue(payload["attempt"])),
		SchemaVersion: 1,
	}
}

func applyTraceContext(proto *pb.TaskEvent, payload map[string]any) {
	trace, ok := payload["trace_context"].(map[string]any)
	if !ok {
		return
	}
	proto.TraceContext = &pb.TraceContext{
		TraceId:      stringValue(trace["trace_id"]),
		SpanId:       stringValue(trace["span_id"]),
		ParentSpanId: stringValue(trace["parent_span_id"]),
		Sampled:      boolValue(trace["sampled"]),
	}
}

func stringValue(raw any) string {
	value, _ := raw.(string)
	return value
}

func boolValue(raw any) bool {
	value, _ := raw.(bool)
	return value
}

func numberValue(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func filterFromWatchRequest(req *pb.WatchTaskRequest) (eventbus.Filter, error) {
	if req == nil {
		return eventbus.Filter{}, routerErr("watch request is required")
	}
	switch f := req.Filter.(type) {
	case *pb.WatchTaskRequest_Topic:
		return eventbus.Filter{Topic: f.Topic}, nil
	case *pb.WatchTaskRequest_BatchId:
		return eventbus.Filter{BatchID: f.BatchId}, nil
	case *pb.WatchTaskRequest_TaskId:
		return eventbus.Filter{TaskID: f.TaskId}, nil
	default:
		return eventbus.Filter{}, routerErr("WatchTask requires oneof topic/batch_id/task_id")
	}
}

func routerErr(msg string) error {
	return &router.Error{Msg: msg}
}
