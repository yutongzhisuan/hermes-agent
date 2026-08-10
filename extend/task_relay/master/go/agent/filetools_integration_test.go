package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/require"
)

func TestMasterFileToolsEndToEnd(t *testing.T) {
	root := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.txt"), []byte("alpha\nbeta\n"), 0o644))

	yaml := `
exec:
  enabled: false
  audit:
    path: ` + auditPath + `
file:
  enabled: true
  root: ` + root + `
  policy:
    deny_paths: ["**/.env"]
`
	cfgPath := filepath.Join(t.TempDir(), "master.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o644))

	fileCfg, err := LoadMasterConfigFile(cfgPath)
	require.NoError(t, err)
	cfg, _, err := MergeFileIntoConfig(Config{MasterSession: "it"}, fileCfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.File)

	tools, err := buildFileTools(cfg, nil)
	require.NoError(t, err)
	require.Len(t, tools, 4)

	byName := map[string]tool.InvokableTool{}
	for _, tl := range tools {
		info, err := tl.Info(context.Background())
		require.NoError(t, err)
		byName[info.Name] = tl.(tool.InvokableTool)
	}
	require.Contains(t, byName, "view")
	require.Contains(t, byName, "write")
	require.Contains(t, byName, "edit")
	require.Contains(t, byName, "multiedit")

	out, err := byName["view"].InvokableRun(context.Background(), `{"path":"hello.txt"}`)
	require.NoError(t, err)
	require.Contains(t, out, "alpha")

	out, err = byName["write"].InvokableRun(context.Background(), `{"path":"new.txt","content":"gamma"}`)
	require.NoError(t, err)
	require.Contains(t, out, "bytes_written")

	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "gamma", string(data))

	out, err = byName["edit"].InvokableRun(context.Background(), `{"path":"new.txt","old_string":"gamma","new_string":"delta"}`)
	require.NoError(t, err)

	data, err = os.ReadFile(filepath.Join(root, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "delta", string(data))

	_, denyErr := byName["view"].InvokableRun(context.Background(), `{"path":".env"}`)
	require.Error(t, denyErr)
	require.Contains(t, denyErr.Error(), "denied")

	auditData, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	s := string(auditData)
	require.Contains(t, s, `"op":"file_view"`)
	require.Contains(t, s, `"op":"file_write"`)
	require.Contains(t, s, `"op":"file_edit"`)
}
