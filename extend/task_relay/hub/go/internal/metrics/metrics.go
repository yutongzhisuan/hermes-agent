package metrics

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
)

var (
	mu         sync.Mutex
	counters   = map[string]float64{}
	gauges     = map[string]float64{}
	histSum    = map[string]float64{}
	histCount  = map[string]float64{}
	typedNames = map[string]string{}
)

// Inc increments a counter metric.
func Inc(name string, labels map[string]string, value float64) {
	if value == 0 {
		value = 1
	}
	mu.Lock()
	defer mu.Unlock()
	counters[formatKey(name, labels)] += value
	typedNames[name] = "counter"
}

// SetGauge sets a gauge metric.
func SetGauge(name string, labels map[string]string, value float64) {
	mu.Lock()
	defer mu.Unlock()
	gauges[formatKey(name, labels)] = value
	typedNames[name] = "gauge"
}

// Observe records a summary observation.
func Observe(name string, labels map[string]string, value float64) {
	mu.Lock()
	defer mu.Unlock()
	key := formatKey(name, labels)
	histSum[key] += value
	histCount[key]++
	typedNames[name] = "summary"
}

// RenderPrometheus renders collected metrics in Prometheus text exposition format.
func RenderPrometheus() string {
	mu.Lock()
	defer mu.Unlock()
	if len(counters)+len(gauges)+len(histSum) == 0 {
		return ""
	}
	names := sortedMetricNames()
	lines := make([]string, 0, len(names)*2+len(histSum)*2)
	emitted := map[string]struct{}{}
	for _, name := range names {
		if _, ok := emitted[name]; !ok {
			lines = append(lines, fmt.Sprintf("# TYPE %s %s", name, typedNames[name]))
			emitted[name] = struct{}{}
		}
	}
	for _, key := range sortedKeys(counters) {
		lines = append(lines, fmt.Sprintf("%s %g", key, counters[key]))
	}
	for _, key := range sortedKeys(gauges) {
		lines = append(lines, fmt.Sprintf("%s %g", key, gauges[key]))
	}
	for _, key := range sortedKeys(histSum) {
		lines = append(lines, fmt.Sprintf("%s_sum %g", key, histSum[key]))
		lines = append(lines, fmt.Sprintf("%s_count %g", key, histCount[key]))
	}
	return strings.Join(lines, "\n") + "\n"
}

// Snapshot returns a flat map of metric keys to values.
func Snapshot() map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]float64, len(counters)+len(gauges)+len(histSum)*2)
	maps.Copy(out, counters)
	maps.Copy(out, gauges)
	for k, v := range histSum {
		out[k+"_sum"] = v
		out[k+"_count"] = histCount[k]
	}
	return out
}

// Reset clears all collected metrics (for tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	counters = map[string]float64{}
	gauges = map[string]float64{}
	histSum = map[string]float64{}
	histCount = map[string]float64{}
	typedNames = map[string]string{}
}

func formatKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, labels[k]))
	}
	return fmt.Sprintf("%s{%s}", name, strings.Join(parts, ","))
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedMetricNames() []string {
	set := map[string]struct{}{}
	for k := range counters {
		set[strings.Split(k, "{")[0]] = struct{}{}
	}
	for k := range gauges {
		set[strings.Split(k, "{")[0]] = struct{}{}
	}
	for k := range histSum {
		set[strings.Split(k, "{")[0]] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
