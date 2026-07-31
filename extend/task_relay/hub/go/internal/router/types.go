package router

import "time"

// Error is a router-level validation or state-machine violation.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

// TaskSpec is the Master-facing dispatch input.
type TaskSpec struct {
	TaskID               string
	Goal                 string
	CallbackTopic        string
	BatchID              string
	TargetWorker         string
	ParamsJSON           string
	ContextJSON          string
	Toolsets             []string
	DependsOn            []string
	AggregateKey         string
	MinResourcesJSON     string
	TraceContextJSON     string
	AllowedWorkerIDsJSON string
	DenyWorkerIDsJSON    string
	ResumeFromCheckpoint string
	Priority             int
	QueueTimeoutSeconds  int
	FirstProgressSeconds int
	TimeoutSeconds       int
	MaxAttempts          int
}

// Task is a persisted Hub row.
type Task struct {
	TaskID                  string
	BatchID                 string
	Goal                    string
	CallbackTopic           string
	Status                  string
	Attempt                 int
	MaxAttempts             int
	WorkerID                string
	ClaimToken              string
	TargetWorker            string
	MasterSessionID         string
	ParamsJSON              string
	ContextJSON             string
	ToolsetsJSON            string
	DependsOnJSON           string
	AggregateKey            string
	MinResourcesJSON        string
	TraceContextJSON        string
	AllowedWorkerIDsJSON    string
	DenyWorkerIDsJSON       string
	ResumeFromCheckpoint    string
	Error                   string
	Priority                int
	QueueTimeoutSeconds     int
	FirstProgressSeconds    int
	TimeoutSeconds          int
	QueueDeadlineAt         time.Time
	FirstProgressDeadlineAt time.Time
	ClaimExpiresAt          time.Time
	StartedAt               time.Time
	CreatedAt               time.Time
	CompletedAt             time.Time
	Summary                 string
	CancelReason            string
}

// Checkpoint is a persisted L1/L2 checkpoint row.
type Checkpoint struct {
	CheckpointID string
	TaskID       string
	Summary      string
	ResumeBlob   []byte
	CheckpointAt time.Time
}

// DispatchResponse mirrors the gRPC dispatch ACK fields used by conformance tests.
type DispatchResponse struct {
	TaskID        string
	CallbackTopic string
	Status        string
	IdempotentHit bool
	Attempt       int
}

// Batch is a persisted batch dispatch row.
type Batch struct {
	BatchID         string
	CallbackTopic   string
	BatchSpecHash   string
	PolicyJSON      string
	CreatedAt       time.Time
	BatchDeadlineAt time.Time
}

// BatchDispatchResponse mirrors the gRPC batch ACK.
type BatchDispatchResponse struct {
	BatchID       string
	CallbackTopic string
	Tasks         []DispatchResponse
	IdempotentHit bool
}
