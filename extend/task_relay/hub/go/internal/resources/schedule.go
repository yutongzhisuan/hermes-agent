package resources

import (
	"encoding/json"
	"sort"
)

// WorkerScheduleView carries fields used for soft scheduling hints.
type WorkerScheduleView struct {
	WorkerID      string
	Region        string
	MaxConcurrent int
	RunningTasks  int
	LoadJSON      string
}

// ParsePreferRegion returns preferred region from task params (soft scheduling hint).
func ParsePreferRegion(paramsJSON string) string {
	if paramsJSON == "" {
		return ""
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		return ""
	}
	for _, key := range []string{"prefer_region", "region"} {
		if v, ok := params[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// SortWorkerViews orders workers by region preference then ascending load.
func SortWorkerViews(paramsJSON string, workers []WorkerScheduleView) []WorkerScheduleView {
	if len(workers) <= 1 {
		return workers
	}
	prefer := ParsePreferRegion(paramsJSON)
	out := append([]WorkerScheduleView(nil), workers...)
	sort.SliceStable(out, func(i, j int) bool {
		lRank := regionRank(out[i], prefer)
		rRank := regionRank(out[j], prefer)
		if lRank != rRank {
			return lRank < rRank
		}
		return workerLoadScore(out[i]) < workerLoadScore(out[j])
	})
	return out
}

func workerLoadScore(view WorkerScheduleView) float64 {
	var load map[string]any
	if view.LoadJSON != "" {
		_ = json.Unmarshal([]byte(view.LoadJSON), &load)
	}
	running := float64(view.RunningTasks)
	if v, ok := asFloat(load["running_tasks"]); ok {
		running = v
	}
	maxConcurrent := view.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	utilization := running / float64(maxConcurrent)
	cpu := floatFromMap(load, "cpu_percent", "cpu")
	memory := floatFromMap(load, "memory_percent", "memory")
	return utilization*1000 + cpu + memory
}

func regionRank(view WorkerScheduleView, preferRegion string) int {
	if preferRegion == "" || view.Region == preferRegion {
		return 0
	}
	return 1
}

func floatFromMap(raw map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if v, ok := asFloat(raw[key]); ok {
			return v
		}
	}
	return 0
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		n, err := t.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}
