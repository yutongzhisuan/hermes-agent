package registry_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
)

func TestIsEligibleForPollRejectsDrainingWorker(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "draining", SessionModes: []string{"A"},
	}
	task := registry.TaskView("", "", "", "")
	require.False(t, registry.IsEligibleForPoll(worker, task, nil))
}

func TestIsEligibleForPollRejectsStaleWorker(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "stale", SessionModes: []string{"A"},
	}
	task := registry.TaskView("", "", "", "")
	require.False(t, registry.IsEligibleForPoll(worker, task, nil))
}

func TestIsEligibleForPollRequiresTargetWorkerMatch(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "idle", SessionModes: []string{"A"},
	}
	task := registry.TaskView("w2", "", "", "")
	require.False(t, registry.IsEligibleForPoll(worker, task, nil))
}

func TestIsEligibleForPollRequiresToolsetSubset(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "idle", SessionModes: []string{"A"},
		Toolsets: []string{"terminal"},
	}
	task := registry.TaskView("", `["terminal","browser"]`, "", "")
	require.False(t, registry.IsEligibleForPoll(worker, task, nil))
}

func TestSupportsModeC(t *testing.T) {
	worker := &registry.Worker{SessionModes: []string{"A", "C"}}
	require.True(t, registry.SupportsMode(worker, "C"))
}
