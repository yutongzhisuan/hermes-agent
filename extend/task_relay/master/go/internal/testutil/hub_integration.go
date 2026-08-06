//go:build integration

package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/client"
)

// HubEnv holds live Hub credentials from the e2e fixture.
type HubEnv struct {
	Addr string
	JWT  string
}

// RequireHubEnv skips the test when HUB_GRPC_ADDR or MASTER_JWT is unset.
func RequireHubEnv(t *testing.T) HubEnv {
	t.Helper()
	addr := os.Getenv("HUB_GRPC_ADDR")
	jwt := os.Getenv("MASTER_JWT")
	if addr == "" || jwt == "" {
		t.Skip("HUB_GRPC_ADDR and MASTER_JWT must be set (run scripts/run_go_master_e2e.sh)")
	}
	return HubEnv{Addr: addr, JWT: jwt}
}

// TestContext returns a timeout-bound context for integration tests.
func TestContext(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), timeout)
}

// NewHubClient dials the Hub and registers cleanup.
func NewHubClient(t *testing.T, ctx context.Context, env HubEnv) *client.Client {
	t.Helper()
	hub, err := client.New(ctx, client.Config{Addr: env.Addr, MasterJWT: env.JWT})
	require.NoError(t, err)
	t.Cleanup(func() { _ = hub.Close() })
	return hub
}
