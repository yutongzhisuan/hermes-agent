package grpcserver

import (
	"encoding/base64"
	"encoding/json"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/contextref"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func mapTaskSpec(spec *pb.TaskSpec) router.TaskSpec {
	if spec == nil {
		return router.TaskSpec{}
	}
	out := router.TaskSpec{
		TaskID:               spec.TaskId,
		Goal:                 spec.Goal,
		CallbackTopic:        spec.CallbackTopic,
		TargetWorker:         spec.TargetWorker,
		Toolsets:             append([]string(nil), spec.Toolsets...),
		DependsOn:            append([]string(nil), spec.DependsOn...),
		AggregateKey:         spec.AggregateKey,
		Priority:             int(spec.Priority),
		QueueTimeoutSeconds:  int(spec.QueueTimeoutSeconds),
		FirstProgressSeconds: int(spec.FirstProgressSeconds),
		TimeoutSeconds:       int(spec.TimeoutSeconds),
		MaxAttempts:          int(spec.MaxAttempts),
	}
	if len(spec.Params) > 0 {
		raw, _ := json.Marshal(spec.Params)
		out.ParamsJSON = string(raw)
	}
	if spec.Context != nil {
		raw, _ := json.Marshal(contextPayloadToMap(spec.Context))
		out.ContextJSON = string(raw)
	}
	if spec.TraceContext != nil {
		raw, _ := json.Marshal(map[string]any{
			"trace_id":       spec.TraceContext.TraceId,
			"span_id":        spec.TraceContext.SpanId,
			"parent_span_id": spec.TraceContext.ParentSpanId,
			"sampled":        spec.TraceContext.Sampled,
		})
		out.TraceContextJSON = string(raw)
	}
	if len(spec.AllowedWorkerIds) > 0 {
		raw, _ := json.Marshal(spec.AllowedWorkerIds)
		out.AllowedWorkerIDsJSON = string(raw)
	}
	if len(spec.DenyWorkerIds) > 0 {
		raw, _ := json.Marshal(spec.DenyWorkerIds)
		out.DenyWorkerIDsJSON = string(raw)
	}
	if spec.MinResources != nil {
		raw, _ := json.Marshal(map[string]any{
			"min_cpu_cores":             spec.MinResources.MinCpuCores,
			"min_memory_gb":             spec.MinResources.MinMemoryGb,
			"requires_gpu":              spec.MinResources.RequiresGpu,
			"required_network_profiles": spec.MinResources.RequiredNetworkProfiles,
		})
		out.MinResourcesJSON = string(raw)
	}
	return out
}

func contextPayloadToMap(ctx *pb.ContextPayload) map[string]any {
	if ctx == nil {
		return map[string]any{}
	}
	switch payload := ctx.Payload.(type) {
	case *pb.ContextPayload_Inline:
		return map[string]any{"inline": payload.Inline}
	case *pb.ContextPayload_InlineGzip:
		return map[string]any{
			"inline_gzip": map[string]any{
				"gzip_data": base64.StdEncoding.EncodeToString(payload.InlineGzip.GzipData),
				"sha256":    payload.InlineGzip.Sha256,
			},
		}
	case *pb.ContextPayload_Ref:
		ref := map[string]any{
			"uri":              payload.Ref.Uri,
			"sha256":           payload.Ref.Sha256,
			"content_encoding": payload.Ref.ContentEncoding,
		}
		if payload.Ref.Signature != "" {
			ref["signature"] = payload.Ref.Signature
		}
		return map[string]any{"ref": ref}
	default:
		return map[string]any{}
	}
}

func validateContextJSON(contextJSON string, cfg router.RouterConfig) error {
	if contextJSON == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(contextJSON), &payload); err != nil {
		return err
	}
	rawRef, ok := payload["ref"].(map[string]any)
	if !ok {
		return nil
	}
	ref, err := contextref.RefFromMap(rawRef)
	if err != nil {
		return err
	}
	if !cfg.RequireSignedContextRef && ref.Signature == "" {
		return nil
	}
	if err := contextref.Verify(ref, cfg.JWTSecret); err != nil {
		return err
	}
	return nil
}

func mapBatchPolicyJSON(policy *pb.BatchPolicy) string {
	if policy == nil {
		return ""
	}
	payload := map[string]any{
		"fail_fast": policy.FailFast,
	}
	if policy.CompletionMode != pb.BatchPolicy_COMPLETION_MODE_UNSPECIFIED {
		payload["completion_mode"] = policy.CompletionMode.String()
	}
	if policy.SuccessThreshold > 0 {
		payload["success_threshold"] = policy.SuccessThreshold
	}
	if policy.BatchTimeoutMs > 0 {
		payload["batch_timeout_ms"] = policy.BatchTimeoutMs
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}
