package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func TestAuditWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log, err := policy.NewAuditLogger(path)
	require.NoError(t, err)
	defer log.Close()

	err = log.Log(policy.AuditEntry{
		Command:  "ls",
		Backend:  "local",
		Decision: "allow",
		ExitCode: 0,
		Stdout:   "file1\nfile2\n",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry))
	require.Equal(t, "ls", entry["command"])
	require.Equal(t, "allow", entry["decision"])
	require.Equal(t, "local", entry["backend"])
	require.NotContains(t, string(data), "file1")
	require.Contains(t, string(data), "sha256:")
	require.Equal(t, float64(12), entry["stdout_len"])
}

func TestAuditFailClosed(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, err := policy.NewAuditLogger(filepath.Join(blocker, "sub", "audit.jsonl"))
	require.Error(t, err)
}
