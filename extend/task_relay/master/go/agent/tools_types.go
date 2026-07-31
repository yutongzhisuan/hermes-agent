package agent

// DispatchTaskInput is the argument schema for dispatch_task.
type DispatchTaskInput struct {
	TaskID        string `json:"task_id" jsonschema:"required,description=Globally unique task identifier"`
	Goal          string `json:"goal" jsonschema:"required,description=Worker execution goal"`
	CallbackTopic string `json:"callback_topic" jsonschema:"required,description=WatchTask topic shared by related tasks"`
	TargetWorker  string `json:"target_worker,omitempty" jsonschema:"description=Optional pinned worker id"`
}

// DispatchTaskOutput summarizes a single dispatch ACK.
type DispatchTaskOutput struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	CallbackTopic string `json:"callback_topic"`
	IdempotentHit bool   `json:"idempotent_hit"`
}

// BatchTaskSpec is one task inside dispatch_batch.
type BatchTaskSpec struct {
	TaskID       string `json:"task_id" jsonschema:"required"`
	Goal         string `json:"goal" jsonschema:"required"`
	TargetWorker string `json:"target_worker,omitempty"`
}

// DispatchBatchInput is the argument schema for dispatch_batch.
type DispatchBatchInput struct {
	BatchID       string          `json:"batch_id" jsonschema:"required,description=Globally unique batch identifier"`
	CallbackTopic string          `json:"callback_topic" jsonschema:"required"`
	Tasks         []BatchTaskSpec `json:"tasks" jsonschema:"required"`
}

// DispatchBatchTaskACK mirrors one task entry in the batch response.
type DispatchBatchTaskACK struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	IdempotentHit bool   `json:"idempotent_hit"`
}

// DispatchBatchOutput summarizes a batch dispatch ACK.
type DispatchBatchOutput struct {
	BatchID       string                 `json:"batch_id"`
	CallbackTopic string                 `json:"callback_topic"`
	Tasks         []DispatchBatchTaskACK `json:"tasks"`
}

// WatchJoinInput is the argument schema for watch_and_join.
type WatchJoinInput struct {
	CallbackTopic    string   `json:"callback_topic" jsonschema:"required"`
	TaskIDs          []string `json:"task_ids" jsonschema:"required"`
	JoinMode         string   `json:"join_mode,omitempty" jsonschema:"description=ALL, ANY, MAJORITY, or THRESHOLD; default ALL"`
	SuccessThreshold int      `json:"success_threshold,omitempty" jsonschema:"description=Required when join_mode is THRESHOLD"`
}

// TaskTerminalSummary is a compact terminal result for the LLM.
type TaskTerminalSummary struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

// WatchJoinOutput is returned after join completes.
type WatchJoinOutput struct {
	Satisfied   bool                  `json:"satisfied"`
	LastEventID int64                 `json:"last_event_id"`
	Results     []TaskTerminalSummary `json:"results"`
}

// GetTaskResultInput is the argument schema for get_task_result.
type GetTaskResultInput struct {
	TaskID                  string `json:"task_id" jsonschema:"required"`
	IncludeLatestCheckpoint bool   `json:"include_latest_checkpoint,omitempty"`
}

// GetTaskResultOutput mirrors Hub GetTaskResult fields needed by the Master.
type GetTaskResultOutput struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CancelTaskInput is the argument schema for cancel_task.
type CancelTaskInput struct {
	TaskID  string `json:"task_id,omitempty"`
	BatchID string `json:"batch_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// CancelTaskOutput summarizes cancellation effects.
type CancelTaskOutput struct {
	CancelledTaskIDs       []string `json:"cancelled_task_ids"`
	AlreadyTerminalTaskIDs []string `json:"already_terminal_task_ids"`
}
