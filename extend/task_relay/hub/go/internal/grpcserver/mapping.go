package grpcserver

import (
	"encoding/json"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
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
	if spec.MinResources != nil {
		raw, _ := json.Marshal(map[string]any{
			"min_cpu_cores":              spec.MinResources.MinCpuCores,
			"min_memory_gb":              spec.MinResources.MinMemoryGb,
			"requires_gpu":               spec.MinResources.RequiresGpu,
			"required_network_profiles":  spec.MinResources.RequiredNetworkProfiles,
		})
		out.MinResourcesJSON = string(raw)
	}
	return out
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
