//go:build integration

package client_test

import (
	"context"
	"os"
	"testing"
	"time"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/join"
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

func TestGoMasterBatchJoinAll(t *testing.T) {
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

	topic := "go-e2e-batch"
	batchID := "go-batch-1"
	taskIDs := []string{"go-e2e-b1", "go-e2e-b2"}
	specs := make([]*pb.TaskSpec, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		specs = append(specs, &pb.TaskSpec{
			TaskId:        taskID,
			Goal:          "go master batch join",
			CallbackTopic: topic,
		})
	}

	_, err = hub.DispatchTaskBatch(ctx, &pb.DispatchTaskBatchRequest{
		BatchId:         batchID,
		Specs:           specs,
		MasterSessionId: "go-e2e-session",
		CallbackTopic:   topic,
	})
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	outcome, err := join.JoinBatch(
		ctx,
		hub,
		client.WatchFilter{Topic: topic},
		taskIDs,
		join.Policy{Mode: join.ModeAll},
	)
	if err != nil {
		t.Fatalf("join batch: %v", err)
	}
	if !outcome.Satisfied || len(outcome.Results) != len(taskIDs) {
		t.Fatalf("expected all terminals, got %+v", outcome)
	}
	for _, taskID := range taskIDs {
		result, ok := outcome.Results[taskID]
		if !ok {
			t.Fatalf("missing result for %s", taskID)
		}
		if result.Status != pb.TaskStatus_TASK_STATUS_COMPLETED {
			t.Fatalf("task %s expected completed, got %v", taskID, result.Status)
		}
	}
}
