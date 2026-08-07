package executor_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/executor"
)

func localExec(t *testing.T) executor.Executor {
	t.Helper()
	e, err := executor.NewLocal(executor.LocalOptions{})
	require.NoError(t, err)
	return e
}

func TestLocalEcho(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "echo hello",
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "hello\n", res.Stdout)
	require.False(t, res.TimedOut)
}

func TestLocalNonZeroExit(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "exit 3",
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.Equal(t, 3, res.ExitCode)
}

func TestLocalTimeout(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "sleep 5",
		Timeout: 200 * time.Millisecond,
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.True(t, res.TimedOut)
}

func TestLocalOutputTruncation(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command:        "seq 1 100000",
		MaxOutputBytes: 256,
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.LessOrEqual(t, int64(len(res.Stdout)), int64(256))
}

func TestLocalEnvFilter(t *testing.T) {
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "echo $EXEC_TEST_VAR",
		Env:     map[string]string{"EXEC_TEST_VAR": "filtered"},
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.Equal(t, "filtered\n", res.Stdout)
}

func TestLocalKillsProcessGroup(t *testing.T) {
	tmp := t.TempDir()
	pgidFile := filepath.Join(tmp, "pgid")
	res, err := localExec(t).Run(context.Background(), executor.Spec{
		Command: "echo $$ > " + pgidFile + "; sleep 60 & wait",
		Timeout: 300 * time.Millisecond,
	}.WithDefaults(10*time.Second, time.Minute, 1<<20))
	require.NoError(t, err)
	require.True(t, res.TimedOut)

	data, err := os.ReadFile(pgidFile)
	require.NoError(t, err)
	pgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)

	err = syscall.Kill(-pgid, 0)
	require.Error(t, err)
	require.Equal(t, syscall.ESRCH, err)
}

func TestLocalSetsidEscapeDoesNotHang(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available (macOS)")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := localExec(t).Run(context.Background(), executor.Spec{
			Command: "setsid sleep 60 & wait",
			Timeout: 200 * time.Millisecond,
		}.WithDefaults(10*time.Second, time.Minute, 1<<20))
		require.NoError(t, err)
		require.True(t, res.TimedOut)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run hung after killing process group — WaitDelay not working")
	}
}
