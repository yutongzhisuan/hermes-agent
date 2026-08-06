package batchpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/batchpolicy"
)

func TestCompletionThresholdAny(t *testing.T) {
	require.True(t, batchpolicy.CompletionThresholdMet(1, 2, map[string]any{"completion_mode": "ANY"}))
}

func TestCompletionThresholdAllNeverMet(t *testing.T) {
	require.False(t, batchpolicy.CompletionThresholdMet(2, 2, map[string]any{"completion_mode": "ALL"}))
}
