package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent"
)

func TestMasterNewRequiresHubAddr(t *testing.T) {
	_, err := agent.New(context.Background(), agent.Config{
		OpenAIAPIKey: "test-key",
	})
	require.Error(t, err)
}

func TestMasterNewRequiresAPIKey(t *testing.T) {
	_, err := agent.New(context.Background(), agent.Config{
		HubAddr: "127.0.0.1:1",
	})
	require.Error(t, err)
}

func TestMasterCloseNilSafe(t *testing.T) {
	var m *agent.Master
	require.NoError(t, m.Close())
}

func TestDefaultModeIsDeep(t *testing.T) {
	require.Equal(t, agent.Mode("deep"), agent.ModeDeep)
	require.Equal(t, agent.Mode("react"), agent.ModeReAct)
}

func TestMasterUnsupportedMode(t *testing.T) {
	_, err := agent.New(context.Background(), agent.Config{
		Mode:      agent.Mode("unknown"),
		ChatModel: &scriptedChatModel{responses: []*schema.Message{schema.AssistantMessage("noop", nil)}},
		Tools:     mustRecordingTools(t, &toolRecorder{}),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported agent mode")
}

// TestMasterReactOrchestratesDispatchThenWatch verifies Master ReAct logic:
// scripted LLM plans remote work via dispatch_batch, waits via watch_and_join,
// then produces a final summary from tool results.
func TestMasterReactOrchestratesDispatchThenWatch(t *testing.T) {
	recorder := &toolRecorder{}
	tools := mustRecordingTools(t, recorder)

	dispatchArgs, err := json.Marshal(agent.DispatchBatchInput{
		BatchID:       "sched-batch",
		CallbackTopic: "sched-topic",
		Tasks: []agent.BatchTaskSpec{
			{TaskID: "sched-priority", Goal: "analyze priority queue"},
			{TaskID: "sched-fairshare", Goal: "analyze fair-share"},
		},
	})
	require.NoError(t, err)

	watchArgs, err := json.Marshal(agent.WatchJoinInput{
		CallbackTopic: "sched-topic",
		TaskIDs:       []string{"sched-priority", "sched-fairshare"},
		JoinMode:      "ALL",
	})
	require.NoError(t, err)

	cm := &scriptedChatModel{
		responses: []*schema.Message{
			assistantToolCall("call-1", "dispatch_batch", string(dispatchArgs)),
			assistantToolCall("call-2", "watch_and_join", string(watchArgs)),
			schema.AssistantMessage(
				"Comparison: priority favors high-urgency jobs; fair-share balances tenants. Both remote tasks completed.",
				nil,
			),
		},
	}

	master, err := agent.New(context.Background(), agent.Config{
		Mode:          agent.ModeReAct,
		ChatModel:     cm,
		Tools:         tools,
		MaxIterations: 8,
		Instruction:   "Coordinate remote Relay tasks. Always dispatch then watch_and_join before summarizing.",
	})
	require.NoError(t, err)
	defer master.Close()

	answer, err := master.Run(context.Background(),
		"Compare priority-queue and fair-share scheduling using remote workers, then summarize.")
	require.NoError(t, err)
	require.Contains(t, answer, "Comparison")
	require.Contains(t, answer, "priority")
	require.Contains(t, answer, "fair-share")

	require.Equal(t, []string{"dispatch_batch", "watch_and_join"}, recorder.names())

	var batch agent.DispatchBatchInput
	require.NoError(t, json.Unmarshal([]byte(recorder.argAt(0)), &batch))
	require.Equal(t, "sched-batch", batch.BatchID)
	require.Equal(t, "sched-topic", batch.CallbackTopic)
	require.Len(t, batch.Tasks, 2)
	require.Equal(t, "sched-priority", batch.Tasks[0].TaskID)
	require.Equal(t, "sched-fairshare", batch.Tasks[1].TaskID)

	var watch agent.WatchJoinInput
	require.NoError(t, json.Unmarshal([]byte(recorder.argAt(1)), &watch))
	require.Equal(t, "sched-topic", watch.CallbackTopic)
	require.Equal(t, []string{"sched-priority", "sched-fairshare"}, watch.TaskIDs)
	require.Equal(t, "ALL", watch.JoinMode)
}

// TestMasterReactSingleTaskPath covers the simpler single-task Master workflow.
func TestMasterReactSingleTaskPath(t *testing.T) {
	recorder := &toolRecorder{}
	tools := mustRecordingTools(t, recorder)

	dispatchArgs, err := json.Marshal(agent.DispatchTaskInput{
		TaskID:        "task-1",
		Goal:          "summarize deadline-aware scheduling",
		CallbackTopic: "single-topic",
	})
	require.NoError(t, err)
	watchArgs, err := json.Marshal(agent.WatchJoinInput{
		CallbackTopic: "single-topic",
		TaskIDs:       []string{"task-1"},
		JoinMode:      "ALL",
	})
	require.NoError(t, err)

	cm := &scriptedChatModel{
		responses: []*schema.Message{
			assistantToolCall("c1", "dispatch_task", string(dispatchArgs)),
			assistantToolCall("c2", "watch_and_join", string(watchArgs)),
			schema.AssistantMessage("Deadline-aware scheduling completed successfully.", nil),
		},
	}

	master, err := agent.New(context.Background(), agent.Config{
		Mode:          agent.ModeReAct,
		ChatModel:     cm,
		Tools:         tools,
		MaxIterations: 6,
	})
	require.NoError(t, err)
	defer master.Close()

	answer, err := master.Run(context.Background(), "Run one remote analysis task and report.")
	require.NoError(t, err)
	require.Contains(t, answer, "Deadline-aware")
	require.Equal(t, []string{"dispatch_task", "watch_and_join"}, recorder.names())
}

func TestMasterRunRequiresFinalMessage(t *testing.T) {
	cm := &scriptedChatModel{responses: []*schema.Message{}}
	master, err := agent.New(context.Background(), agent.Config{
		Mode:      agent.ModeReAct,
		ChatModel: cm,
		Tools:     mustRecordingTools(t, &toolRecorder{}),
	})
	require.NoError(t, err)
	defer master.Close()

	_, err = master.Run(context.Background(), "anything")
	require.Error(t, err)
}

type toolCallRecord struct {
	Name string
	Args string
}

type toolRecorder struct {
	mu    sync.Mutex
	calls []toolCallRecord
}

func (r *toolRecorder) record(name, args string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, toolCallRecord{Name: name, Args: args})
}

func (r *toolRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = c.Name
	}
	return out
}

func (r *toolRecorder) argAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[i].Args
}

func mustRecordingTools(t *testing.T, recorder *toolRecorder) []tool.BaseTool {
	t.Helper()
	dispatchTask, err := toolutils.InferTool(
		"dispatch_task",
		"Dispatch one TaskSpec to the Relay Hub",
		func(_ context.Context, in agent.DispatchTaskInput) (agent.DispatchTaskOutput, error) {
			raw, _ := json.Marshal(in)
			recorder.record("dispatch_task", string(raw))
			return agent.DispatchTaskOutput{
				TaskID:        in.TaskID,
				Status:        "TASK_STATUS_PENDING",
				CallbackTopic: in.CallbackTopic,
			}, nil
		},
	)
	require.NoError(t, err)

	dispatchBatch, err := toolutils.InferTool(
		"dispatch_batch",
		"Dispatch multiple TaskSpecs sharing one callback topic",
		func(_ context.Context, in agent.DispatchBatchInput) (agent.DispatchBatchOutput, error) {
			raw, _ := json.Marshal(in)
			recorder.record("dispatch_batch", string(raw))
			acks := make([]agent.DispatchBatchTaskACK, 0, len(in.Tasks))
			for _, task := range in.Tasks {
				acks = append(acks, agent.DispatchBatchTaskACK{
					TaskID: task.TaskID,
					Status: "TASK_STATUS_PENDING",
				})
			}
			return agent.DispatchBatchOutput{
				BatchID:       in.BatchID,
				CallbackTopic: in.CallbackTopic,
				Tasks:         acks,
			}, nil
		},
	)
	require.NoError(t, err)

	watch, err := toolutils.InferTool(
		"watch_and_join",
		"Watch a callback topic and join TERMINAL results for task ids",
		func(_ context.Context, in agent.WatchJoinInput) (agent.WatchJoinOutput, error) {
			raw, _ := json.Marshal(in)
			recorder.record("watch_and_join", string(raw))
			results := make([]agent.TaskTerminalSummary, 0, len(in.TaskIDs))
			for _, id := range in.TaskIDs {
				results = append(results, agent.TaskTerminalSummary{
					TaskID:  id,
					Status:  "TASK_STATUS_COMPLETED",
					Summary: "stub completed: " + id,
				})
			}
			return agent.WatchJoinOutput{Satisfied: true, Results: results}, nil
		},
	)
	require.NoError(t, err)

	getResult, err := toolutils.InferTool(
		"get_task_result",
		"Fetch the latest Hub result for a task id",
		func(_ context.Context, in agent.GetTaskResultInput) (agent.GetTaskResultOutput, error) {
			raw, _ := json.Marshal(in)
			recorder.record("get_task_result", string(raw))
			return agent.GetTaskResultOutput{
				TaskID:  in.TaskID,
				Status:  "TASK_STATUS_COMPLETED",
				Summary: "stub result",
			}, nil
		},
	)
	require.NoError(t, err)

	cancel, err := toolutils.InferTool(
		"cancel_task",
		"Cancel a running or pending task or batch",
		func(_ context.Context, in agent.CancelTaskInput) (agent.CancelTaskOutput, error) {
			raw, _ := json.Marshal(in)
			recorder.record("cancel_task", string(raw))
			ids := []string{}
			if in.TaskID != "" {
				ids = append(ids, in.TaskID)
			}
			return agent.CancelTaskOutput{CancelledTaskIDs: ids}, nil
		},
	)
	require.NoError(t, err)

	return []tool.BaseTool{dispatchTask, dispatchBatch, watch, getResult, cancel}
}

type scriptedChatModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	idx       int
}

func (m *scriptedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return schema.AssistantMessage("", nil), nil
	}
	msg := m.responses[m.idx]
	m.idx++
	return msg, nil
}

func (m *scriptedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func assistantToolCall(id, name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}})
}
