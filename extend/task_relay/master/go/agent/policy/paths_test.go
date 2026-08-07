package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o644))
	return root
}

func eval(t *testing.T, root string, rules policy.PathRules) policy.PathEvaluator {
	t.Helper()
	e, err := policy.NewPathEvaluator(root, rules)
	require.NoError(t, err)
	return e
}

func TestPathInsideRootAllowed(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{})
	require.Equal(t, policy.Allow, e.EvaluatePath("src/main.go"))
}

func TestPathAbsoluteInsideRootAllowed(t *testing.T) {
	root := newRoot(t)
	e := eval(t, root, policy.PathRules{})
	require.Equal(t, policy.Allow, e.EvaluatePath(filepath.Join(root, "src/main.go")))
}

func TestPathEscapeDenied(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{})
	require.Equal(t, policy.Deny, e.EvaluatePath("../outside.txt"))
	require.Equal(t, policy.Deny, e.EvaluatePath("/etc/passwd"))
}

func TestPathDenyList(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{DenyList: []string{".env", "**/*.pem"}})
	require.Equal(t, policy.Deny, e.EvaluatePath(".env"))
	require.Equal(t, policy.Deny, e.EvaluatePath("certs/server.pem"))
}

func TestPathAllowListWhitelistMode(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{AllowList: []string{"src/**"}})
	require.Equal(t, policy.Allow, e.EvaluatePath("src/main.go"))
	require.Equal(t, policy.Deny, e.EvaluatePath("README.md"))
}

func TestPathAllowListAbsoluteOverride(t *testing.T) {
	root := newRoot(t)
	shared := t.TempDir()
	// Allow globs match the symlink-resolved path form, so resolve first
	// (on macOS t.TempDir() returns /var/... which resolves to /private/var/...).
	shared, err := filepath.EvalSymlinks(shared)
	require.NoError(t, err)
	e := eval(t, root, policy.PathRules{AllowList: []string{"src/**", shared + "/**"}})
	require.Equal(t, policy.Allow, e.EvaluatePath(filepath.Join(shared, "data.json")))
	require.Equal(t, policy.Deny, e.EvaluatePath("/etc/passwd"))
}

func TestPathSymlinkEscapeDenied(t *testing.T) {
	root := newRoot(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
	e := eval(t, root, policy.PathRules{})
	require.Equal(t, policy.Deny, e.EvaluatePath("link/secret.txt"))
}

func TestPathNonexistentFileAllowed(t *testing.T) {
	e := eval(t, newRoot(t), policy.PathRules{})
	require.Equal(t, policy.Allow, e.EvaluatePath("new/dir/file.txt"))
}

func TestPathAllowListSymlinkPrefixNotBypassed(t *testing.T) {
	root := newRoot(t)
	target := t.TempDir() // will be symlinked
	require.NoError(t, os.WriteFile(filepath.Join(target, "secret.txt"), []byte("x"), 0o644))
	shared := filepath.Join(t.TempDir(), "shared")
	require.NoError(t, os.Symlink(target, shared))

	// Allow glob written against the symlinked (raw) path form.
	e := eval(t, root, policy.PathRules{AllowList: []string{"src/**", shared + "/**"}})
	// The glob matches the raw form but the resolved path escapes to target.
	// Allow must NOT honor the raw form: the resolved target/secret.txt is
	// outside every allowed prefix, so the decision must be Deny.
	require.Equal(t, policy.Deny, e.EvaluatePath(filepath.Join(shared, "secret.txt")))
}
