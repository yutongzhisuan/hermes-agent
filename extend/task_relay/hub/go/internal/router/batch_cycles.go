package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BatchDeadlineFromPolicy derives batch deadline from policy JSON.
func BatchDeadlineFromPolicy(policyJSON string, createdAt time.Time) time.Time {
	if policyJSON == "" {
		return time.Time{}
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		return time.Time{}
	}
	timeoutMS, ok := policy["batch_timeout_ms"]
	if !ok {
		timeoutMS = policy["batchTimeoutMs"]
	}
	ms, ok := asFloat(timeoutMS)
	if !ok || ms <= 0 {
		return time.Time{}
	}
	return createdAt.Add(time.Duration(ms) * time.Millisecond)
}

// CheckDependencyCycles rejects cyclic depends_on graphs in a batch.
func CheckDependencyCycles(specs []TaskSpec) error {
	graph := make(map[string][]string, len(specs))
	for _, spec := range specs {
		graph[spec.TaskID] = append([]string(nil), spec.DependsOn...)
	}
	visiting := make(map[string]struct{})
	visited := make(map[string]struct{})

	var visit func(node string, stack []string) error
	visit = func(node string, stack []string) error {
		if _, ok := visiting[node]; ok {
			return &Error{Msg: fmt.Sprintf("dependency cycle detected: %s -> %s", joinPath(stack), node)}
		}
		if _, ok := visited[node]; ok {
			return nil
		}
		visiting[node] = struct{}{}
		for _, dep := range graph[node] {
			if err := visit(dep, append(stack, node)); err != nil {
				return err
			}
		}
		delete(visiting, node)
		visited[node] = struct{}{}
		return nil
	}
	for node := range graph {
		if _, ok := visited[node]; !ok {
			if err := visit(node, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func joinPath(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		out.WriteString(" -> " + parts[i])
	}
	return out.String()
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
