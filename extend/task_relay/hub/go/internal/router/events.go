package router

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
)

const (
	EventKindStatus     = "STATUS"
	EventKindProgress   = "PROGRESS"
	EventKindTerminal   = "TERMINAL"
	EventKindCheckpoint = "CHECKPOINT"
	EventKindAggregate  = "AGGREGATE"
)

// EventPublisher persists and fans out watch events.
type EventPublisher interface {
	Publish(ctx context.Context, event *TaskEvent) (*TaskEvent, error)
}

// EventEmitter emits task watch events through the event bus.
type EventEmitter interface {
	EmitStatus(ctx context.Context, task *Task, status string) error
	EmitProgress(ctx context.Context, task *Task, summary string) error
	EmitTerminal(ctx context.Context, task *Task, status, summary, errMsg string) error
	EmitCheckpoint(ctx context.Context, task *Task, checkpointID, summary, fieldsJSON string) (*TaskEvent, error)
	EmitAggregate(ctx context.Context, task *Task, payload map[string]any) error
}

type busEmitter struct {
	bus EventPublisher
}

// NewBusEmitter returns an EventEmitter backed by the hub event bus.
func NewBusEmitter(bus EventPublisher) EventEmitter {
	return &busEmitter{bus: bus}
}

func (e *busEmitter) EmitStatus(ctx context.Context, task *Task, status string) error {
	_, err := e.bus.Publish(ctx, &TaskEvent{
		CallbackTopic: task.CallbackTopic,
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		Kind:          EventKindStatus,
		PayloadJSON:   encodeEventPayload(task, map[string]any{"status": status, "attempt": task.Attempt}),
	})
	return err
}

func (e *busEmitter) EmitProgress(ctx context.Context, task *Task, summary string) error {
	_, err := e.bus.Publish(ctx, &TaskEvent{
		CallbackTopic: task.CallbackTopic,
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		Kind:          EventKindProgress,
		PayloadJSON:   encodeEventPayload(task, map[string]any{"summary": summary, "attempt": task.Attempt}),
	})
	return err
}

func (e *busEmitter) EmitTerminal(ctx context.Context, task *Task, status, summary, errMsg string) error {
	_, err := e.bus.Publish(ctx, &TaskEvent{
		CallbackTopic: task.CallbackTopic,
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		Kind:          EventKindTerminal,
		PayloadJSON: encodeEventPayload(task, map[string]any{
			"status": status, "summary": summary, "error": errMsg, "attempt": task.Attempt,
		}),
	})
	return err
}

func (e *busEmitter) EmitCheckpoint(
	ctx context.Context,
	task *Task,
	checkpointID, summary, fieldsJSON string,
) (*TaskEvent, error) {
	return e.bus.Publish(ctx, &TaskEvent{
		CallbackTopic: task.CallbackTopic,
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		Kind:          EventKindCheckpoint,
		PayloadJSON: encodeEventPayload(task, map[string]any{
			"checkpoint_id": checkpointID,
			"summary":       summary,
			"fields_json":   fieldsJSON,
			"attempt":       task.Attempt,
		}),
	})
}

func (e *busEmitter) EmitAggregate(ctx context.Context, task *Task, payload map[string]any) error {
	_, err := e.bus.Publish(ctx, &TaskEvent{
		CallbackTopic: task.CallbackTopic,
		BatchID:       task.BatchID,
		Kind:          EventKindAggregate,
		PayloadJSON:   mustJSON(payload),
	})
	return err
}

func encodeEventPayload(task *Task, payload map[string]any) string {
	if task != nil && task.TraceContextJSON != "" {
		payload = copyPayload(payload)
		var trace map[string]any
		if err := json.Unmarshal([]byte(task.TraceContextJSON), &trace); err == nil && trace != nil {
			payload["trace_context"] = trace
		}
	}
	return mustJSON(payload)
}

func copyPayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	maps.Copy(out, payload)
	return out
}

func mustJSON(payload map[string]any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":"encode payload: %s"}`, err.Error())
	}
	return string(raw)
}

func claimProgressSummary(goal string) string {
	const maxLen = 40
	if len(goal) > maxLen {
		goal = goal[:maxLen]
	}
	return "claimed, starting " + goal
}
