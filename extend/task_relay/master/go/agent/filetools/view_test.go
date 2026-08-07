package filetools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/filetools"
	"github.com/infa/task_relay/master/agent/policy"
)

func setup(t *testing.T) (*filetools.Deps, string) {
	t.Helper()
	root := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte(content), 0o644))
	paths, err := policy.NewPathEvaluator(root, policy.PathRules{DenyList: []string{".env"}})
	require.NoError(t, err)
	audit, err := policy.NewAuditLogger(filepath.Join(t.TempDir(), "audit.jsonl"))
	require.NoError(t, err)
	return &filetools.Deps{
		Paths: paths, Audit: audit,
		MaxReadBytes: 1 << 20, MaxWriteBytes: 1 << 20,
		Session: "test",
	}, root
}

func TestViewWholeFile(t *testing.T) {
	deps, _ := setup(t)
	out, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "a.txt"})
	require.NoError(t, err)
	require.Contains(t, out.Content, "line1")
	require.Equal(t, 5, out.TotalLines)
	require.False(t, out.Truncated)
}

func TestViewLineRange(t *testing.T) {
	deps, _ := setup(t)
	out, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "a.txt", Offset: 2, Limit: 2})
	require.NoError(t, err)
	require.NotContains(t, out.Content, "line1")
	require.Contains(t, out.Content, "line2")
	require.Contains(t, out.Content, "line3")
	require.NotContains(t, out.Content, "line4")
}

func TestViewDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: ".env"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestViewEscapeDenied(t *testing.T) {
	deps, _ := setup(t)
	_, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "../outside.txt"})
	require.Error(t, err)
}

func TestViewByteTruncation(t *testing.T) {
	deps, root := setup(t)
	big := make([]byte, 4096)
	for i := range big {
		big[i] = 'x'
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644))
	deps.MaxReadBytes = 1024
	out, err := filetools.NewViewTool(deps).Run(context.Background(), filetools.ViewInput{Path: "big.txt"})
	require.NoError(t, err)
	require.True(t, out.Truncated)
	require.LessOrEqual(t, len(out.Content), 1024+100)
}
