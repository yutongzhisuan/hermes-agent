//go:build integration

package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/infa/hermes-agent/extend/task_relay/master/go/agent"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
)

func TestRelayToolsDispatchWatchJoin(t *testing.T) {
	addr := os.Getenv("HUB_GRPC_ADDR")
	jwt := os.Getenv("MASTER_JWT")
	if addr == "" || jwt == "" {
		t.Skip("HUB_GRPC_ADDR and MASTER_JWT must be set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hub, err := client.New(ctx, client.Config{Addr: addr, MasterJWT: jwt})
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	defer hub.Close()

	tools, err := (&agent.RelayTools{
		Hub:           hub,
		MasterSession: "tools-integration",
	}).Build()
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools))
	}

	taskID := "go-tools-1"
	topic := "go-tools-topic"
	dispatchTool, err := findInvokableTool(tools, "dispatch_task")
	if err != nil {
		t.Fatalf("dispatch tool: %v", err)
	}
	dispatchArgs, _ := json.Marshal(agent.DispatchTaskInput{
		TaskID:        taskID,
		Goal:          "relay tools integration",
		CallbackTopic: topic,
	})
	dispatchRaw, err := dispatchTool.InvokableRun(ctx, string(dispatchArgs))
	if err != nil {
		t.Fatalf("dispatch run: %v", err)
	}
	var dispatchOut agent.DispatchTaskOutput
	if err := json.Unmarshal([]byte(dispatchRaw), &dispatchOut); err != nil {
		t.Fatalf("decode dispatch output: %v", err)
	}
	if dispatchOut.TaskID != taskID {
		t.Fatalf("unexpected task id: %s", dispatchOut.TaskID)
	}

	watchTool, err := findInvokableTool(tools, "watch_and_join")
	if err != nil {
		t.Fatalf("watch tool: %v", err)
	}
	watchArgs, _ := json.Marshal(agent.WatchJoinInput{
		CallbackTopic: topic,
		TaskIDs:       []string{taskID},
		JoinMode:      "ALL",
	})
	watchRaw, err := watchTool.InvokableRun(ctx, string(watchArgs))
	if err != nil {
		t.Fatalf("watch run: %v", err)
	}
	var joinOut agent.WatchJoinOutput
	if err := json.Unmarshal([]byte(watchRaw), &joinOut); err != nil {
		t.Fatalf("decode watch output: %v", err)
	}
	if !joinOut.Satisfied || len(joinOut.Results) != 1 {
		t.Fatalf("unexpected join output: %+v", joinOut)
	}
	if joinOut.Results[0].Status != "TASK_STATUS_COMPLETED" {
		t.Fatalf("expected completed, got %s", joinOut.Results[0].Status)
	}
}

func findInvokableTool(tools []tool.BaseTool, name string) (tool.InvokableTool, error) {
	for _, base := range tools {
		it, ok := base.(tool.InvokableTool)
		if !ok {
			continue
		}
		info, err := base.Info(context.Background())
		if err != nil {
			return nil, err
		}
		if info.Name == name {
			return it, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found", name)
}
