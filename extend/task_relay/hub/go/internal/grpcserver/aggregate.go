package grpcserver

import pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"

func aggregateToProto(payload map[string]any) *pb.AggregateResult {
	result := &pb.AggregateResult{SchemaVersion: 1}
	if v, ok := payload["batch_id"].(string); ok {
		result.BatchId = v
	}
	if v, ok := payload["aggregate_key"].(string); ok {
		result.AggregateKey = v
	}
	if v, ok := payload["summary"].(string); ok {
		result.Summary = v
	}
	if ids, ok := payload["task_ids"].([]any); ok {
		for _, item := range ids {
			if s, ok := item.(string); ok {
				result.TaskIds = append(result.TaskIds, s)
			}
		}
	}
	if counts, ok := payload["status_counts"].(map[string]any); ok {
		result.StatusCounts = make(map[string]int32, len(counts))
		for key, value := range counts {
			switch n := value.(type) {
			case float64:
				result.StatusCounts[key] = int32(n)
			case int:
				result.StatusCounts[key] = int32(n)
			}
		}
	}
	if version, ok := payload["schema_version"].(float64); ok {
		result.SchemaVersion = int32(version)
	}
	return result
}
