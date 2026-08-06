package batchpolicy

import (
	"encoding/json"
	"strconv"
	"strings"
)

var modeAliases = map[string]string{
	"0": "UNSPECIFIED", "1": "ALL", "2": "ANY", "3": "MAJORITY", "4": "THRESHOLD",
	"COMPLETION_MODE_UNSPECIFIED": "UNSPECIFIED", "COMPLETION_MODE_ALL": "ALL",
	"COMPLETION_MODE_ANY": "ANY", "COMPLETION_MODE_MAJORITY": "MAJORITY",
	"COMPLETION_MODE_THRESHOLD": "THRESHOLD",
}

// NormalizeCompletionMode returns an upper-case completion mode from policy JSON.
func NormalizeCompletionMode(policy map[string]any) string {
	raw, ok := policy["completion_mode"]
	if !ok {
		raw = policy["completionMode"]
	}
	if raw == nil {
		return "ALL"
	}
	switch v := raw.(type) {
	case float64:
		if mapped, ok := modeAliases[strconv.Itoa(int(v))]; ok {
			return mapped
		}
	case int:
		if mapped, ok := modeAliases[strconv.Itoa(v)]; ok {
			return mapped
		}
	case json.Number:
		if mapped, ok := modeAliases[v.String()]; ok {
			return mapped
		}
	}
	text := strings.ToUpper(strings.TrimSpace(toString(raw)))
	if mapped, ok := modeAliases[text]; ok {
		return mapped
	}
	return strings.TrimPrefix(text, "COMPLETION_MODE_")
}

// CompletionThresholdMet reports whether batch success criteria are satisfied.
func CompletionThresholdMet(completed, total int, policy map[string]any) bool {
	if total == 0 {
		return false
	}
	mode := NormalizeCompletionMode(policy)
	switch mode {
	case "UNSPECIFIED", "ALL", "":
		return false
	case "ANY":
		return completed >= 1
	case "MAJORITY":
		return completed > total/2
	case "THRESHOLD":
		return completed >= max(1, thresholdValue(policy))
	default:
		return false
	}
}

func thresholdValue(policy map[string]any) int {
	raw, ok := policy["success_threshold"]
	if !ok {
		raw = policy["successThreshold"]
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return 1
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}
