package agent_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestExecConfigMerge(t *testing.T) {
	file := &agent.MasterFileConfig{
		Exec: &agent.ExecFileConfig{
			Enabled:        true,
			DefaultBackend: "local",
			Policy: &agent.ExecPolicyFileConfig{
				Mode:      "deny_by_default",
				AllowList: []string{"ls"},
			},
		},
	}
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{}, file)
	require.NoError(t, err)
	require.NotNil(t, cfg.Exec)
	require.True(t, cfg.Exec.Enabled)
	require.Equal(t, []string{"ls"}, cfg.Exec.Policy.AllowList)
}

func TestExecConfigDefaults(t *testing.T) {
	cfg := agent.ExecConfig{}.WithDefaults()
	require.Equal(t, 60*time.Second, cfg.Limits.TimeoutDefault)
	require.Equal(t, 10*time.Minute, cfg.Limits.TimeoutMax)
	require.Equal(t, int64(1<<20), cfg.Limits.MaxOutputBytes)
	require.Contains(t, cfg.AuditPath, "exec-audit.jsonl")
}

func TestExecConfigBadDurationFails(t *testing.T) {
	file := &agent.MasterFileConfig{
		Exec: &agent.ExecFileConfig{
			Enabled: true,
			Limits:  &agent.ExecLimitsFileConfig{TimeoutDefault: "not-a-duration"},
		},
	}
	_, _, err := agent.MergeFileIntoConfig(agent.Config{}, file)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout_default")
	require.Contains(t, err.Error(), "not-a-duration")
}
