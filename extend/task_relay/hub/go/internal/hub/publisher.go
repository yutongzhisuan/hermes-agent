package hub

import (
	"context"

	"github.com/infa/task_relay/hub/internal/orchestrator"
	"github.com/infa/task_relay/hub/internal/router"
)

type eventPublisher struct {
	emitter router.EventEmitter
}

func newEventPublisher(emitter router.EventEmitter) orchestrator.TerminalPublisher {
	return &eventPublisher{emitter: emitter}
}

func (p *eventPublisher) PublishTerminal(task *router.Task) {
	if p == nil || p.emitter == nil || task == nil {
		return
	}
	_ = p.emitter.EmitTerminal(context.Background(), task, task.Status, task.Summary, task.Error)
}

func (p *eventPublisher) PublishAggregate(task *router.Task, payload map[string]any) {
	if p == nil || p.emitter == nil || task == nil {
		return
	}
	_ = p.emitter.EmitAggregate(context.Background(), task, payload)
}
