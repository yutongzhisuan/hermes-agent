package client_test

import (
	"context"
	"testing"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
)

func TestAttachTraceToSpecUsesExplicitContext(t *testing.T) {
	tc := &pb.TraceContext{TraceId: "trace-1", SpanId: "span-1", Sampled: true}
	ctx := client.WithTraceContext(context.Background(), tc)
	spec := &pb.TaskSpec{TaskId: "t1", Goal: "g"}
	client.AttachTraceToSpec(spec, ctx)
	if spec.GetTraceContext().GetTraceId() != "trace-1" {
		t.Fatalf("trace not attached: %+v", spec.GetTraceContext())
	}
}

func TestAttachTraceToSpecDoesNotOverwrite(t *testing.T) {
	spec := &pb.TaskSpec{
		TaskId:        "t1",
		TraceContext:  &pb.TraceContext{TraceId: "existing"},
	}
	ctx := client.WithTraceContext(context.Background(), &pb.TraceContext{TraceId: "new"})
	client.AttachTraceToSpec(spec, ctx)
	if spec.GetTraceContext().GetTraceId() != "existing" {
		t.Fatalf("expected existing trace to remain")
	}
}
