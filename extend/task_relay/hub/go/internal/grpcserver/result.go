package grpcserver

import (
	"encoding/json"

	"github.com/infa/task_relay/hub/internal/resources"
	"github.com/infa/task_relay/hub/internal/router"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

func taskToProto(task *router.Task) *pb.TaskResult {
	if task == nil {
		return &pb.TaskResult{}
	}
	result := &pb.TaskResult{
		TaskId:             task.TaskID,
		Status:             statusToProto(task.Status),
		Summary:            task.Summary,
		ResultText:         task.ResultJSON,
		Error:              task.Error,
		WorkerId:           task.WorkerID,
		Attempt:            int32(task.Attempt),
		MaxAttempts:        int32(task.MaxAttempts),
		BatchId:            task.BatchID,
		LatestCheckpointId: task.ResumeFromCheckpoint,
		SchemaVersion:      1,
	}
	if !task.StartedAt.IsZero() {
		result.StartedAt = task.StartedAt.UnixMilli()
	}
	if !task.CompletedAt.IsZero() {
		result.CompletedAt = task.CompletedAt.UnixMilli()
	}
	if fields := fieldsFromJSON(task.FieldsJSON); fields != nil {
		result.Fields = fields
	}
	if usage := usageFromJSON(task.UsageJSON); usage != nil {
		result.Usage = usage
	}
	return result
}

func existingResultToProto(result *router.ExistingResult) *pb.TaskResult {
	if result == nil {
		return nil
	}
	proto := &pb.TaskResult{
		TaskId:             result.TaskID,
		Status:             statusToProto(result.Status),
		Summary:            result.Summary,
		ResultText:         result.ResultText,
		Error:              result.Error,
		WorkerId:           result.WorkerID,
		Attempt:            int32(result.Attempt),
		MaxAttempts:        int32(result.MaxAttempts),
		BatchId:            result.BatchID,
		LatestCheckpointId: result.LatestCheckpointID,
		SchemaVersion:      1,
	}
	if !result.StartedAt.IsZero() {
		proto.StartedAt = result.StartedAt.UnixMilli()
	}
	if !result.CompletedAt.IsZero() {
		proto.CompletedAt = result.CompletedAt.UnixMilli()
	}
	if fields := fieldsFromJSON(result.FieldsJSON); fields != nil {
		proto.Fields = fields
	}
	if usage := usageFromJSON(result.UsageJSON); usage != nil {
		proto.Usage = usage
	}
	return proto
}

func dispatchResponseToProto(resp *router.DispatchResponse) *pb.DispatchTaskResponse {
	if resp == nil {
		return &pb.DispatchTaskResponse{}
	}
	out := &pb.DispatchTaskResponse{
		TaskId:        resp.TaskID,
		CallbackTopic: resp.CallbackTopic,
		Status:        statusToProto(resp.Status),
		IdempotentHit: resp.IdempotentHit,
		Attempt:       int32(resp.Attempt),
	}
	if resp.ExistingResult != nil {
		out.ExistingResult = existingResultToProto(resp.ExistingResult)
	}
	return out
}

func fieldsFromJSON(raw string) *pb.TaskFields {
	if raw == "" {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil || data == nil {
		return nil
	}
	fields := &pb.TaskFields{Version: int32(numberValue(data["version"]))}
	if fields.Version == 0 {
		fields.Version = 1
	}
	for _, item := range sliceOfMaps(data["metrics"]) {
		fields.Metrics = append(fields.Metrics, &pb.Metric{
			Name:         stringValue(item["name"]),
			Value:        numberValue(item["value"]),
			Unit:         stringValue(item["unit"]),
			Description:  stringValue(item["description"]),
			OriginTaskId: stringValue(item["origin_task_id"]),
		})
	}
	for _, item := range sliceOfMaps(data["tags"]) {
		fields.Tags = append(fields.Tags, &pb.KeyValue{
			Key:   stringValue(item["key"]),
			Value: stringValue(item["value"]),
		})
	}
	if report, ok := data["report"].(string); ok {
		fields.Report = report
	}
	if extensions, ok := data["extensions"].(map[string]any); ok {
		fields.Extensions = map[string][]byte{}
		for key, value := range extensions {
			switch typed := value.(type) {
			case string:
				fields.Extensions[key] = []byte(typed)
			case []byte:
				fields.Extensions[key] = typed
			}
		}
	}
	return fields
}

func usageFromJSON(raw string) *pb.TaskUsage {
	if raw == "" {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil || data == nil {
		return nil
	}
	prompt := int32(numberValue(data["prompt_tokens"]))
	completion := int32(numberValue(data["completion_tokens"]))
	total := int32(numberValue(data["total_tokens"]))
	if total == 0 {
		total = prompt + completion
	}
	return &pb.TaskUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		ApiCalls:         int32(numberValue(data["api_calls"])),
		ToolCalls:        int32(numberValue(data["tool_calls"])),
		WallSeconds:      numberValue(data["wall_seconds"]),
		CostUsd:          numberValue(data["cost_usd"]),
		Model:            stringValue(data["model"]),
	}
}

func sliceOfMaps(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if typed, ok := item.(map[string]any); ok {
			out = append(out, typed)
		}
	}
	return out
}

func protoResourceRequirements(req *pb.ResourceRequirements) *resources.TaskRequirements {
	if req == nil {
		return nil
	}
	return &resources.TaskRequirements{
		MinCPUCores:             int(req.MinCpuCores),
		MinMemoryGB:             int(req.MinMemoryGb),
		RequiresGPU:             req.RequiresGpu,
		RequiredNetworkProfiles: append([]string(nil), req.RequiredNetworkProfiles...),
	}
}
