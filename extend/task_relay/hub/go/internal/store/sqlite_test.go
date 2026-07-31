package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

func TestSQLiteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	task := &router.Task{
		TaskID:        "s1",
		Goal:          "goal",
		CallbackTopic: "topic",
		Status:        router.StatusPending,
		CreatedAt:     time.Unix(100, 0),
	}
	if err := db.InsertTask(ctx, task); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := db.GetTask(ctx, "s1")
	if err != nil || got == nil || got.Goal != "goal" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
}
