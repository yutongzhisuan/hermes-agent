//go:build integration

package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/store"
)

// OpenTestPostgres opens a Postgres store when TASK_RELAY_TEST_PG is set.
func OpenTestPostgres(t *testing.T) router.Store {
	t.Helper()
	url := os.Getenv("TASK_RELAY_TEST_PG")
	if url == "" {
		t.Skip("TASK_RELAY_TEST_PG not set")
	}
	pg, err := store.OpenPostgres(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Close() })
	require.NoError(t, store.TruncatePostgresTables(context.Background(), pg))
	return pg
}
