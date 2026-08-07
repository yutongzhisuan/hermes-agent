package executor_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/executor"
)

func TestSpecDefaults(t *testing.T) {
	s := executor.Spec{Command: "ls"}.WithDefaults(60*time.Second, 10*time.Minute, 1<<20)
	require.Equal(t, 60*time.Second, s.Timeout)
	require.Equal(t, int64(1<<20), s.MaxOutputBytes)
}

func TestSpecTimeoutClamped(t *testing.T) {
	s := executor.Spec{Command: "ls", Timeout: time.Hour}.WithDefaults(60*time.Second, 10*time.Minute, 1<<20)
	require.Equal(t, 10*time.Minute, s.Timeout)
}
