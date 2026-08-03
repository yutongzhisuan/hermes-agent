package client

import (
	"context"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"go.opentelemetry.io/otel/trace"
)

type traceContextKey struct{}

// WithTraceContext stores a protobuf TraceContext on the context.
func WithTraceContext(ctx context.Context, tc *pb.TraceContext) context.Context {
	if tc == nil {
		return ctx
	}
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// TraceContextFromContext returns a stored protobuf TraceContext, if any.
func TraceContextFromContext(ctx context.Context) *pb.TraceContext {
	if ctx == nil {
		return nil
	}
	tc, _ := ctx.Value(traceContextKey{}).(*pb.TraceContext)
	return tc
}

// TraceContextFromOTel maps the active OTel span context to protobuf TraceContext.
func TraceContextFromOTel(ctx context.Context) *pb.TraceContext {
	if ctx == nil {
		return nil
	}
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if !sc.IsValid() {
		return nil
	}
	return &pb.TraceContext{
		TraceId: sc.TraceID().String(),
		SpanId:  sc.SpanID().String(),
		Sampled: sc.IsSampled(),
	}
}

// ResolveTraceContext prefers an explicit protobuf trace, then OTel span context.
func ResolveTraceContext(ctx context.Context) *pb.TraceContext {
	if tc := TraceContextFromContext(ctx); tc != nil {
		return tc
	}
	return TraceContextFromOTel(ctx)
}

// AttachTraceToSpec copies the resolved trace context onto a TaskSpec when unset.
func AttachTraceToSpec(spec *pb.TaskSpec, ctx context.Context) {
	if spec == nil || spec.TraceContext != nil {
		return
	}
	if tc := ResolveTraceContext(ctx); tc != nil {
		spec.TraceContext = tc
	}
}
