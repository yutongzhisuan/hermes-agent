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
	ResultJSON              string
	FieldsJSON              string
	UsageJSON               string
	AllowRedispatch         bool
}

// Checkpoint is a persisted L1/L2 checkpoint row.
type Checkpoint struct {
	CheckpointID string
	TaskID       string
	EventID      int64
	Summary      string
	FieldsJSON   string
	ResumeBlob   []byte
	CheckpointAt time.Time
	LeaseUntil   time.Time
}

// CompleteInput carries terminal completion payload fields.
type CompleteInput struct {
	ResultJSON string
	FieldsJSON string
	UsageJSON  string
	Error      string
}

// ExistingResult mirrors a prior terminal task result for idempotent dispatch.
type ExistingResult struct {
	TaskID               string
	Status               string
	Summary              string
	ResultText           string
	Error                string
	WorkerID             string
	Attempt              int
	MaxAttempts          int
	BatchID              string
	LatestCheckpointID   string
	StartedAt            time.Time
	CompletedAt          time.Time
	FieldsJSON           string
	UsageJSON            string
}

// TaskEvent is a persisted row in the global event log.
type TaskEvent struct {
	EventID       int64
	CallbackTopic string
	TaskID        string
	BatchID       string
	Kind          string
	PayloadJSON   string
	EventAt       time.Time
}

// EventFilter selects events for WatchTask replay.
type EventFilter struct {
	Topic        string
	BatchID      string
	TaskID       string
	AfterEventID int64
	Limit        int
}

// Worker is a persisted worker registry row.
type Worker struct {
	WorkerID         string
	WakeURL          string
	SessionModes     string
	CapabilitiesJSON string
	ResourcesJSON    string
	LoadJSON         string
	MaxConcurrent    int
	CreditAvailable  int
	RunningTasks     int
	LastAnnounceAt   time.Time
	LastHeartbeatAt  time.Time
	LastSeenAt       time.Time
	Status           string
	OnlineSessionID  string
	DrainRequested   bool
}

// DispatchResponse mirrors the gRPC dispatch ACK fields used by conformance tests.
type DispatchResponse struct {
	TaskID         string
	CallbackTopic  string
	Status         string
	IdempotentHit  bool
	Attempt        int
	ExistingResult *ExistingResult
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
