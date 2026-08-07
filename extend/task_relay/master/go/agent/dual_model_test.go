package agent_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestSmallModelMerge(t *testing.T) {
	file := &agent.MasterFileConfig{
		OpenAI: &agent.OpenAIFileConfig{
			APIKey:     "k",
			Model:      "big",
			SmallModel: "small",
		},
	}
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{}, file)
	require.NoError(t, err)
	require.Equal(t, "small", cfg.OpenAISmallModel)
}

func TestSmallModelEmptyByDefault(t *testing.T) {
	cfg, _, err := agent.MergeFileIntoConfig(agent.Config{}, &agent.MasterFileConfig{
		OpenAI: &agent.OpenAIFileConfig{APIKey: "k", Model: "big"},
	})
	require.NoError(t, err)
	require.Empty(t, cfg.OpenAISmallModel)
}
