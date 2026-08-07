package filetools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/filetools"
)

func TestEditUniqueReplace(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "a.txt", OldString: "line3", NewString: "LINE3",
	})
	require.NoError(t, err)
	require.Equal(t, 1, out.Replacements)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Contains(t, string(data), "LINE3")
}

func TestEditNotFound(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "a.txt", OldString: "nonexistent", NewString: "x",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestEditAmbiguous(t *testing.T) {
	deps, root := setup(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "dup.txt"), []byte("foo\nfoo\n"), 0o644))
	_, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "dup.txt", OldString: "foo", NewString: "bar",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "multiple")
}

func TestEditReplaceAll(t *testing.T) {
	deps, root := setup(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "dup.txt"), []byte("foo\nfoo\n"), 0o644))
	out, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: "dup.txt", OldString: "foo", NewString: "bar", ReplaceAll: true,
	})
	require.NoError(t, err)
	require.Equal(t, 2, out.Replacements)
}

func TestMultiEditAtomic(t *testing.T) {
	deps, root := setup(t)
	_, err := filetools.NewMultiEditTool(deps).Run(context.Background(), filetools.MultiEditInput{
		Path: "a.txt",
		Edits: []filetools.EditOp{
			{OldString: "line1", NewString: "L1"},
			{OldString: "nonexistent", NewString: "X"},
		},
	})
	require.Error(t, err)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Contains(t, string(data), "line1")
}

func TestMultiEditSuccess(t *testing.T) {
	deps, root := setup(t)
	out, err := filetools.NewMultiEditTool(deps).Run(context.Background(), filetools.MultiEditInput{
		Path: "a.txt",
		Edits: []filetools.EditOp{
			{OldString: "line1", NewString: "L1"},
			{OldString: "line5", NewString: "L5"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 2, out.Replacements)
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Contains(t, string(data), "L1")
	require.Contains(t, string(data), "L5")
}

func TestEditDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewEditTool(deps).Run(context.Background(), filetools.EditInput{
		Path: ".env", OldString: "a", NewString: "b",
	})
	require.Error(t, err)
}
