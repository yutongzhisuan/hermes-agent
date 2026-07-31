package hub

import (
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/orchestrator"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

type eventPublisher struct {
	bus *eventbus.Bus
}

func newEventPublisher(bus *eventbus.Bus) orchestrator.TerminalPublisher {
	return &eventPublisher{bus: bus}
}

func (p *eventPublisher) PublishTerminal(task *router.Task) {
	if p == nil || p.bus == nil || task == nil {
		return
	}
	p.bus.Publish(eventbus.Event{
		TaskID: task.TaskID, BatchID: task.BatchID, CallbackTopic: task.CallbackTopic,
		Kind: eventbus.KindTerminal, Status: task.Status, Summary: task.Summary,
	})
}

func (p *eventPublisher) PublishAggregate(event eventbus.Event) {
	if p == nil || p.bus == nil {
		return
	}
	event.Kind = eventbus.KindAggregate
	p.bus.Publish(event)
}
