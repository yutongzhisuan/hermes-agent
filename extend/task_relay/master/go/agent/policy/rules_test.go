package policy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func rules() policy.Rules {
	return policy.Rules{
		Mode:         policy.ModeDenyByDefault,
		AllowList:    []string{"ls", "git status"},
		DenyList:     []string{"rm -rf", "sudo"},
		ApprovalList: []string{"git push"},
	}
}

func TestDenyListWins(t *testing.T) {
	require.Equal(t, policy.Deny, policy.NewEvaluator(rules()).Evaluate("ls; rm -rf /"))
}

func TestAllowList(t *testing.T) {
	require.Equal(t, policy.Allow, policy.NewEvaluator(rules()).Evaluate("ls -la"))
	require.Equal(t, policy.Allow, policy.NewEvaluator(rules()).Evaluate("git status"))
}

func TestApprovalList(t *testing.T) {
	require.Equal(t, policy.NeedsApproval, policy.NewEvaluator(rules()).Evaluate("git push origin main"))
}

func TestDenyByDefaultFallback(t *testing.T) {
	require.Equal(t, policy.Deny, policy.NewEvaluator(rules()).Evaluate("curl example.com"))
}

func TestAllowWithDenyListMode(t *testing.T) {
	r := rules()
	r.Mode = policy.ModeAllowWithDenyList
	require.Equal(t, policy.Allow, policy.NewEvaluator(r).Evaluate("curl example.com"))
}

func TestEmptyCommandDenied(t *testing.T) {
	require.Equal(t, policy.Deny, policy.NewEvaluator(rules()).Evaluate("   "))
}
