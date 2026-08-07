package webtools

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func newDownloadDeps(t *testing.T, root string, rules policy.PathRules, mutate func(*Deps)) *Deps {
	t.Helper()
	paths, err := policy.NewPathEvaluator(root, rules)
	require.NoError(t, err)
	return newTestDeps(t, func(d *Deps) {
		d.Paths = paths
		if mutate != nil {
			mutate(d)
		}
	})
}

func assertNoTmpFiles(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".tmp-") ||
			strings.Contains(e.Name(), ".tmp-"), "leftover tmp file %s", e.Name())
	}
}

func TestDownloadSuccess(t *testing.T) {
	body := make([]byte, 4096)
	_, err := rand.Read(body)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := newDownloadDeps(t, root, policy.PathRules{}, nil)
	tool := NewDownloadTool(deps)

	out, err := tool.Run(context.Background(), DownloadInput{URL: srv.URL, Path: "sub/out.bin"})
	require.NoError(t, err)
	assert.Equal(t, int64(len(body)), out.BytesWritten)
	assert.Equal(t, http.StatusOK, out.StatusCode)
	assert.False(t, out.Truncated)

	expected := filepath.Join(deps.Paths.Root(), "sub", "out.bin")
	assert.Equal(t, expected, out.Path)
	got, err := os.ReadFile(expected)
	require.NoError(t, err)
	assert.Equal(t, body, got)
	assertNoTmpFiles(t, root)
}

func TestDownloadDeniedPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secret")
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := newDownloadDeps(t, root, policy.PathRules{DenyList: []string{".env"}}, nil)
	tool := NewDownloadTool(deps)

	_, err := tool.Run(context.Background(), DownloadInput{URL: srv.URL, Path: ".env"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
	_, statErr := os.Stat(filepath.Join(root, ".env"))
	assert.True(t, os.IsNotExist(statErr))
	assertNoTmpFiles(t, root)
}

func TestDownloadEscapeDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := newDownloadDeps(t, root, policy.PathRules{}, nil)
	tool := NewDownloadTool(deps)

	_, err := tool.Run(context.Background(), DownloadInput{URL: srv.URL, Path: "../out.bin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
	_, statErr := os.Stat(filepath.Join(root, "..", "out.bin"))
	assert.True(t, os.IsNotExist(statErr))
	assertNoTmpFiles(t, root)
}

func TestDownloadHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := newDownloadDeps(t, root, policy.PathRules{}, nil)
	tool := NewDownloadTool(deps)

	_, err := tool.Run(context.Background(), DownloadInput{URL: srv.URL, Path: "out.bin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	_, statErr := os.Stat(filepath.Join(root, "out.bin"))
	assert.True(t, os.IsNotExist(statErr))
	assertNoTmpFiles(t, root)
}

func TestDownloadTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := newDownloadDeps(t, root, policy.PathRules{}, func(d *Deps) {
		d.MaxBytes = 1024
	})
	tool := NewDownloadTool(deps)

	out, err := tool.Run(context.Background(), DownloadInput{URL: srv.URL, Path: "big.bin"})
	require.NoError(t, err)
	assert.True(t, out.Truncated)
	assert.Equal(t, int64(1024), out.BytesWritten)

	info, err := os.Stat(filepath.Join(root, "big.bin"))
	require.NoError(t, err)
	assert.Equal(t, int64(1024), info.Size())
	assertNoTmpFiles(t, root)
}

func TestDownloadURDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := newDownloadDeps(t, root, policy.PathRules{}, func(d *Deps) {
		d.DomainDenyList = []string{"127.0.0.1"}
	})
	tool := NewDownloadTool(deps)

	_, err := tool.Run(context.Background(), DownloadInput{URL: srv.URL, Path: "out.bin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
	_, statErr := os.Stat(filepath.Join(root, "out.bin"))
	assert.True(t, os.IsNotExist(statErr))
	assertNoTmpFiles(t, root)
}
