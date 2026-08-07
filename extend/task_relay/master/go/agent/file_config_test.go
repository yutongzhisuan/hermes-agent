package agent_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestFileConfigMerge(t *testing.T) {
	file := &agent.MasterFileConfig{
		File: &agent.FileToolsFileConfig{
			Enabled: true,
			Root:    "/srv/work",
			Policy: &agent.FilePolicyFileConfig{
				AllowPaths: []string{"src/**"},
				DenyPaths:  []string{".env"},
			},
		},
	}
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{}, file)
	require.NoError(t, err)
	require.NotNil(t, cfg.File)
	require.True(t, cfg.File.Enabled)
	require.Equal(t, "/srv/work", cfg.File.Root)
	require.Equal(t, []string{".env"}, cfg.File.Policy.DenyList)
}

func TestFileConfigDefaults(t *testing.T) {
	cfg := agent.FileToolsConfig{}.WithDefaults("/fallback/root")
	require.Equal(t, "/fallback/root", cfg.Root)
	require.Equal(t, int64(1<<20), cfg.MaxReadBytes)
	require.Equal(t, int64(1<<20), cfg.MaxWriteBytes)
}
