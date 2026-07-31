package registry_test

import (
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
)

func TestIsEligibleForPollRejectsDrainingWorker(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "draining", SessionModes: []string{"A"},
	}
	task := registry.TaskView("", "")
	if registry.IsEligibleForPoll(worker, task, nil) {
		t.Fatal("expected draining worker to be ineligible")
	}
}

func TestIsEligibleForPollRejectsStaleWorker(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "stale", SessionModes: []string{"A"},
	}
	task := registry.TaskView("", "")
	if registry.IsEligibleForPoll(worker, task, nil) {
		t.Fatal("expected stale worker to be ineligible")
	}
}

func TestIsEligibleForPollRequiresTargetWorkerMatch(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "idle", SessionModes: []string{"A"},
	}
	task := registry.TaskView("w2", "")
	if registry.IsEligibleForPoll(worker, task, nil) {
		t.Fatal("expected target worker mismatch to be ineligible")
	}
}

func TestIsEligibleForPollRequiresToolsetSubset(t *testing.T) {
	worker := &registry.Worker{
		WorkerID: "w1", Status: "idle", SessionModes: []string{"A"},
		Toolsets: []string{"terminal"},
	}
	task := registry.TaskView("", `["terminal","browser"]`)
	if registry.IsEligibleForPoll(worker, task, nil) {
		t.Fatal("expected missing toolset to be ineligible")
	}
}

func TestSupportsModeC(t *testing.T) {
	worker := &registry.Worker{SessionModes: []string{"A", "C"}}
	if !registry.SupportsMode(worker, "C") {
		t.Fatal("expected worker to support Mode C")
	}
}
