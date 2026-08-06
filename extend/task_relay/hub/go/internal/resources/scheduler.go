package resources

import (
	"encoding/json"
)

// WorkerView carries resource fields used for min_resources matching.
type WorkerView struct {
	ResourcesJSON string
}

// TaskRequirements carries parsed min_resources for a task.
type TaskRequirements struct {
	MinCPUCores             int
	MinMemoryGB             int
	RequiresGPU             bool
	RequiredNetworkProfiles []string
}

// ParseMinResources decodes min_resources JSON or returns nil when unset.
func ParseMinResources(minResourcesJSON string) *TaskRequirements {
	if minResourcesJSON == "" {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(minResourcesJSON), &raw); err != nil {
		return nil
	}
	req := &TaskRequirements{}
	if v, ok := asInt(raw["min_cpu_cores"]); ok {
		req.MinCPUCores = v
	}
	if v, ok := asInt(raw["min_memory_gb"]); ok {
		req.MinMemoryGB = v
	}
	if b, ok := raw["requires_gpu"].(bool); ok {
		req.RequiresGPU = b
	}
	if profiles, ok := raw["required_network_profiles"].([]any); ok {
		for _, item := range profiles {
			if s, ok := item.(string); ok && s != "" {
				req.RequiredNetworkProfiles = append(req.RequiredNetworkProfiles, s)
			}
		}
	}
	return req
}

// WorkerMeetsResources returns true when worker resources satisfy hard gates.
func WorkerMeetsResources(worker WorkerView, requirements *TaskRequirements) bool {
	if requirements == nil {
		return true
	}
	var resources map[string]any
	if worker.ResourcesJSON != "" {
		_ = json.Unmarshal([]byte(worker.ResourcesJSON), &resources)
	}
	if requirements.MinCPUCores > 0 {
		cpu := firstInt(resources, "cpu_cores", "cpu")
		if cpu < requirements.MinCPUCores {
			return false
		}
	}
	if requirements.MinMemoryGB > 0 {
		mem := firstInt(resources, "memory_gb", "memory")
		if mem < requirements.MinMemoryGB {
			return false
		}
	}
	if requirements.RequiresGPU {
		if firstInt(resources, "gpu_count", "gpu") < 1 {
			return false
		}
	}
	if len(requirements.RequiredNetworkProfiles) > 0 {
		profiles := networkProfiles(resources)
		for _, required := range requirements.RequiredNetworkProfiles {
			if !profiles[required] {
				return false
			}
		}
	}
	return true
}

func firstInt(resources map[string]any, keys ...string) int {
	for _, key := range keys {
		if v, ok := asInt(resources[key]); ok {
			return v
		}
	}
	return 0
}

func networkProfiles(resources map[string]any) map[string]bool {
	set := make(map[string]bool)
	if raw, ok := resources["network_profiles"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				set[s] = true
			}
		}
	}
	if profile, ok := resources["network_profile"].(string); ok && profile != "" {
		set[profile] = true
	}
	return set
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}
