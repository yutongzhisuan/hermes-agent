package webtools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func newTestDeps(t *testing.T, mutate func(*Deps)) *Deps {
	t.Helper()
	audit, err := policy.NewAuditLogger(filepath.Join(t.TempDir(), "audit.jsonl"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = audit.Close() })
	deps := &Deps{
		Audit:                audit,
		AllowPrivateNetworks: true,
		MaxBytes:             1 << 20,
		Timeout:              5 * time.Second,
		Session:              "test-session",
	}
	if mutate != nil {
		mutate(deps)
	}
	return deps
}

func TestFetchTextPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "plain body")
	}))
	defer srv.Close()

	tool := NewFetchTool(newTestDeps(t, nil))
	out, err := tool.Run(context.Background(), FetchInput{URL: srv.URL})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, out.StatusCode)
	assert.Equal(t, "plain body", out.Content)
	assert.False(t, out.Truncated)
	assert.Equal(t, srv.URL, out.URL)
}

func TestFetchHTMLStripped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><style>x</style></head><body><p>Hello</p><script>evil()</script><b>World</b></body></html>`)
	}))
	defer srv.Close()

	tool := NewFetchTool(newTestDeps(t, nil))
	out, err := tool.Run(context.Background(), FetchInput{URL: srv.URL})
	require.NoError(t, err)
	assert.Contains(t, out.Content, "Hello")
	assert.Contains(t, out.Content, "World")
	assert.NotContains(t, out.Content, "evil()")
	assert.NotContains(t, out.Content, "x")
}

func TestFetchTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("a", 4096))
	}))
	defer srv.Close()

	tool := NewFetchTool(newTestDeps(t, func(d *Deps) { d.MaxBytes = 1024 }))
	out, err := tool.Run(context.Background(), FetchInput{URL: srv.URL})
	require.NoError(t, err)
	assert.True(t, out.Truncated)
	assert.Len(t, out.Content, 1024)
}

func TestFetchDomainDenied(t *testing.T) {
	tool := NewFetchTool(newTestDeps(t, func(d *Deps) {
		d.DomainDenyList = []string{"evil.com"}
	}))
	_, err := tool.Run(context.Background(), FetchInput{URL: "http://evil.com/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
}

func TestFetchDomainAllowList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	tool := NewFetchTool(newTestDeps(t, func(d *Deps) {
		d.DomainAllowList = []string{"example.com"}
	}))
	_, err := tool.Run(context.Background(), FetchInput{URL: srv.URL})
	require.Error(t, err)
}

func TestFetchPrivateIP(t *testing.T) {
	private := []string{"10.0.0.1", "127.0.0.1", "100.64.1.1", "::1", "fe80::1", "169.254.169.254"}
	for _, s := range private {
		assert.True(t, isPrivateIP(netip.MustParseAddr(s)), s)
	}
	public := []string{"8.8.8.8", "1.1.1.1"}
	for _, s := range public {
		assert.False(t, isPrivateIP(netip.MustParseAddr(s)), s)
	}
}

func TestFetchRedirectRevalidated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.com/steal", http.StatusFound)
	}))
	defer srv.Close()

	tool := NewFetchTool(newTestDeps(t, func(d *Deps) {
		d.DomainDenyList = []string{"evil.com"}
	}))
	_, err := tool.Run(context.Background(), FetchInput{URL: srv.URL})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
}
