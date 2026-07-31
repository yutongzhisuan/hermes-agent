//go:build integration

package client_test

import (
	"context"
	"os"
	"testing"
	"time"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
)

func TestGoMasterDispatchWatchTerminal(t *testing.T) {
	addr := os.Getenv("HUB_GRPC_ADDR")
	jwt := os.Getenv("MASTER_JWT")
	if addr == "" || jwt == "" {
		t.Skip("HUB_GRPC_ADDR and MASTER_JWT must be set (run scripts/run_go_master_e2e.sh)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hub, err := client.New(ctx, client.Config{Addr: addr, MasterJWT: jwt})
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	defer hub.Close()

	taskID := "go-e2e-1"
	topic := "go-e2e-topic"
	_, err = hub.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        taskID,
		Goal:          "go master sdk e2e",
		CallbackTopic: topic,
	}, "go-e2e-session", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	stream, err := hub.Watch(ctx, client.WatchFilter{Topic: topic})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}

	snap, err := client.CollectTerminals(ctx, stream, []string{taskID})
	if err != nil {
		t.Fatalf("collect terminals: %v", err)
	}

	result, ok := snap.Results[taskID]
	if !ok {
		t.Fatalf("missing terminal result for %s", taskID)
	}
	if result.Status != pb.TaskStatus_TASK_STATUS_COMPLETED {
		t.Fatalf("expected completed, got %v", result.Status)
	}
}
