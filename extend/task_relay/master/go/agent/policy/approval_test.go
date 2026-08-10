package policy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/policy"
)

func TestWebhookApproved(t *testing.T) {
	var gotBody policy.ApprovalRequest
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		gotContentType = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"approved": true}`))
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 5 * time.Second}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{
		JobID: "job-1", Command: "git push origin main", Session: "sess-1",
	})
	require.NoError(t, err)
	require.True(t, approved)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, policy.ApprovalRequest{JobID: "job-1", Command: "git push origin main", Session: "sess-1"}, gotBody)
}

func TestWebhookRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"approved": false}`))
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 5 * time.Second}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{JobID: "j", Command: "c", Session: "s"})
	require.NoError(t, err)
	require.False(t, approved)
}

func TestWebhookHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 5 * time.Second}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{})
	require.Error(t, err)
	require.False(t, approved)
	require.Contains(t, err.Error(), "500")
}

func TestWebhookMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 5 * time.Second}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{})
	require.Error(t, err)
	require.False(t, approved)
	require.Contains(t, err.Error(), "malformed")
}

func TestWebhookMalformedMissingApproved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 5 * time.Second}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{})
	require.Error(t, err)
	require.False(t, approved)
	require.Contains(t, err.Error(), "malformed")
}

func TestWebhookOversizedBodyDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"approved": true` + strings.Repeat(" ", 8192)))
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 5 * time.Second}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{})
	require.Error(t, err)
	require.False(t, approved)
}

func TestWebhookTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Second)
		_, _ = w.Write([]byte(`{"approved": true}`))
	}))
	defer srv.Close()

	svc := &policy.WebhookApproval{URL: srv.URL, Timeout: 100 * time.Millisecond}
	approved, err := svc.RequestApproval(context.Background(), policy.ApprovalRequest{})
	require.Error(t, err)
	require.False(t, approved)
}

func TestWebhookDefaultTimeout(t *testing.T) {
	svc := &policy.WebhookApproval{URL: "http://127.0.0.1:1/unreachable"}
	require.Equal(t, 120*time.Second, svc.EffectiveTimeout())
}
