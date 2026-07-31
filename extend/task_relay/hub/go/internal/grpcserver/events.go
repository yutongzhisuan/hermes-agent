package grpcserver

import (
	"encoding/json"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func eventToProto(event eventbus.Event) *pb.TaskEvent {
	proto := &pb.TaskEvent{
		EventId:         event.EventID,
		EventAt:         event.EventAt.UnixMilli(),
		TaskId:          event.TaskID,
		BatchId:         event.BatchID,
		ProgressSummary: event.ProgressSummary,
	}
	switch event.Kind {
	case eventbus.KindStatus:
		proto.Kind = pb.TaskEventKind_TASK_EVENT_KIND_STATUS
	case eventbus.KindProgress:
		proto.Kind = pb.TaskEventKind_TASK_EVENT_KIND_PROGRESS
	case eventbus.KindTerminal:
		proto.Kind = pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL
	case eventbus.KindAggregate:
		proto.Kind = pb.TaskEventKind_TASK_EVENT_KIND_AGGREGATE
		if event.AggregateJSON != "" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(event.AggregateJSON), &payload); err == nil {
				proto.Aggregate = aggregateToProto(payload)
			}
		}
	}
	if event.Status != "" || event.Summary != "" {
		proto.Result = &pb.TaskResult{
			TaskId:        event.TaskID,
			Status:        statusToProto(event.Status),
			Summary:       event.Summary,
			SchemaVersion: 1,
		}
	}
	return proto
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

func (s *Server) publishStatus(task *router.Task) {
	if s.bus == nil || task == nil {
		return
	}
	s.bus.Publish(eventbus.Event{
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		CallbackTopic: task.CallbackTopic,
		Kind:          eventbus.KindStatus,
		Status:        task.Status,
		Summary:       task.Summary,
	})
}

func (s *Server) publishProgress(task *router.Task, summary string) {
	if s.bus == nil || task == nil {
		return
	}
	s.bus.Publish(eventbus.Event{
		TaskID:          task.TaskID,
		BatchID:         task.BatchID,
		CallbackTopic:   task.CallbackTopic,
		Kind:            eventbus.KindProgress,
		ProgressSummary: summary,
		Status:          task.Status,
	})
}

func (s *Server) publishTerminal(task *router.Task) {
	if s.bus == nil || task == nil {
		return
	}
	s.bus.Publish(eventbus.Event{
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		CallbackTopic: task.CallbackTopic,
		Kind:          eventbus.KindTerminal,
		Status:        task.Status,
		Summary:       task.Summary,
	})
}
