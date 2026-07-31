package grpcserver

import (
	"context"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/resources"
)

// ListWorkers returns workers visible to the Master.
func (s *Server) ListWorkers(
	ctx context.Context,
	req *pb.ListWorkersRequest,
) (*pb.ListWorkersResponse, error) {
	if req == nil {
		req = &pb.ListWorkersRequest{}
	}
	requireToolsets := make(map[string]struct{}, len(req.RequireToolsets))
	for _, toolset := range req.RequireToolsets {
		requireToolsets[toolset] = struct{}{}
	}

	requireResources := protoResourceRequirements(req.GetRequireResources())

	resp := &pb.ListWorkersResponse{}
	for _, worker := range s.registry.List(req.OnlySchedulable) {
		if len(requireToolsets) > 0 && !toolsetsSuperset(worker.Toolsets, requireToolsets) {
			continue
		}
		if requireResources != nil && !resources.WorkerMeetsResources(
			resources.WorkerView{ResourcesJSON: worker.ResourcesJSON},
			requireResources,
		) {
			continue
		}
		resp.Workers = append(resp.Workers, workerToProto(worker))
	}
	return resp, nil
}

func workerToProto(worker registry.Worker) *pb.WorkerInfo {
	info := &pb.WorkerInfo{
		WorkerId:       worker.WorkerID,
		Status:         worker.Status,
		Toolsets:       append([]string(nil), worker.Toolsets...),
		Os:             worker.OS,
		Arch:           worker.Arch,
		Region:         worker.Region,
		MaxConcurrent:  int32(worker.MaxConcurrent),
		RunningTasks:   int32(worker.RunningTasks),
		WakeUrlPresent: worker.WakeURL != "",
	}
	for _, mode := range worker.SessionModes {
		switch mode {
		case "A", "a":
			info.SessionModes = append(info.SessionModes, pb.SessionMode_SESSION_MODE_A)
		case "B", "b":
			info.SessionModes = append(info.SessionModes, pb.SessionMode_SESSION_MODE_B)
		case "C", "c":
			info.SessionModes = append(info.SessionModes, pb.SessionMode_SESSION_MODE_C)
		}
	}
	if !worker.LastAnnounce.IsZero() {
		info.LastAnnounceAt = worker.LastAnnounce.UnixMilli()
	}
	if !worker.LastHeartbeat.IsZero() {
		info.LastHeartbeatAt = worker.LastHeartbeat.UnixMilli()
	}
	return info
}

func toolsetsSuperset(workerToolsets []string, required map[string]struct{}) bool {
	available := make(map[string]struct{}, len(workerToolsets))
	for _, toolset := range workerToolsets {
		available[toolset] = struct{}{}
	}
	for toolset := range required {
		if _, ok := available[toolset]; !ok {
			return false
		}
	}
	return true
}
