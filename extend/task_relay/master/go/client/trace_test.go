package client_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
)

func TestAttachTraceToSpecUsesExplicitContext(t *testing.T) {
	tc := &pb.TraceContext{TraceId: "trace-1", SpanId: "span-1", Sampled: true}
	ctx := client.WithTraceContext(context.Background(), tc)
	spec := &pb.TaskSpec{TaskId: "t1", Goal: "g"}
	client.AttachTraceToSpec(spec, ctx)
	require.Equal(t, "trace-1", spec.GetTraceContext().GetTraceId())
}

func TestAttachTraceToSpecDoesNotOverwrite(t *testing.T) {
	spec := &pb.TaskSpec{
		TaskId:       "t1",
		TraceContext: &pb.TraceContext{TraceId: "existing"},
	}
	ctx := client.WithTraceContext(context.Background(), &pb.TraceContext{TraceId: "new"})
	client.AttachTraceToSpec(spec, ctx)
	require.Equal(t, "existing", spec.GetTraceContext().GetTraceId())
}
