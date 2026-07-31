package batchpolicy_test

import (
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/batchpolicy"
)

func TestCompletionThresholdAny(t *testing.T) {
	if !batchpolicy.CompletionThresholdMet(1, 2, map[string]any{"completion_mode": "ANY"}) {
		t.Fatal("expected ANY threshold met")
	}
}

func TestCompletionThresholdAllNeverMet(t *testing.T) {
	if batchpolicy.CompletionThresholdMet(2, 2, map[string]any{"completion_mode": "ALL"}) {
		t.Fatal("expected ALL mode to never early-complete")
	}
}
