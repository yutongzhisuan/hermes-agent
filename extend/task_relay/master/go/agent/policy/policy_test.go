package policy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func TestDecisionString(t *testing.T) {
	require.Equal(t, "allow", policy.Allow.String())
	require.Equal(t, "deny", policy.Deny.String())
	require.Equal(t, "needs_approval", policy.NeedsApproval.String())
}
