package batchpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/batchpolicy"
)

func TestCompletionThresholdAny(t *testing.T) {
	require.True(t, batchpolicy.CompletionThresholdMet(1, 2, map[string]any{"completion_mode": "ANY"}))
}

func TestCompletionThresholdAllNeverMet(t *testing.T) {
	require.False(t, batchpolicy.CompletionThresholdMet(2, 2, map[string]any{"completion_mode": "ALL"}))
}
