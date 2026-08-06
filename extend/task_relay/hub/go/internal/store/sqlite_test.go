package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
)

func TestSQLiteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	db, err := store.OpenSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	task := &router.Task{
		TaskID:        "s1",
		Goal:          "goal",
		CallbackTopic: "topic",
		Status:        router.StatusPending,
		CreatedAt:     time.Unix(100, 0),
	}
	require.NoError(t, db.InsertTask(ctx, task))

	got, err := db.GetTask(ctx, "s1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "goal", got.Goal)
}
