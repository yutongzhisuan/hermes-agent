package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/require"
)

func TestMasterExecEndToEnd(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	yaml := `
exec:
  enabled: true
  policy:
    mode: deny_by_default
    allow_list: ["echo"]
  audit:
    path: ` + auditPath + `
`
	cfgPath := filepath.Join(t.TempDir(), "master.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o644))

	fileCfg, err := LoadMasterConfigFile(cfgPath)
	require.NoError(t, err)
	cfg, _, err := MergeFileIntoConfig(Config{MasterSession: "it"}, fileCfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.Exec)
	require.True(t, cfg.Exec.Enabled)
	require.Equal(t, auditPath, cfg.Exec.AuditPath)

	bashTool, err := buildBashTool(cfg, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, bashTool)

	invokable, ok := bashTool.(tool.InvokableTool)
	require.True(t, ok)
	outJSON, err := invokable.InvokableRun(context.Background(), `{"command":"echo e2e"}`)
	require.NoError(t, err)
	var out BashOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &out))
	require.Equal(t, "e2e\n", out.Stdout)

	data, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"command":"echo e2e"`)
	require.Contains(t, string(data), `"decision":"allow"`)
}

func TestBuildBashToolRemoteDefaultRequiresHub(t *testing.T) {
	cfg := Config{
		MasterSession: "it",
		Exec: &ExecConfig{
			Enabled:        true,
			DefaultBackend: "remote",
		},
	}
	_, err := buildBashTool(cfg, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hub")
}
