package filetools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/filetools"
)

func TestWriteNewFile(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: "new/dir/out.txt", Content: "hello",
	})
	require.NoError(t, err)
	require.True(t, out.Created)
	require.Equal(t, 5, out.BytesWritten)
	data, err := os.ReadFile(filepath.Join(root, "new/dir/out.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

func TestWriteOverwrite(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: "a.txt", Content: "replaced",
	})
	require.NoError(t, err)
	require.False(t, out.Created)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Equal(t, "replaced", string(data))
}

func TestWriteDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: ".env", Content: "x",
	})
	require.Error(t, err)
}

func TestWriteSizeLimit(t *testing.T) {
	deps, _ := setup(t)
	deps.MaxWriteBytes = 4
	_, err := filetools.NewWriteTool(deps).Run(context.Background(), filetools.WriteInput{
		Path: "big.txt", Content: "too-long-content",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_write_bytes")
}
