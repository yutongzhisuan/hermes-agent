package router

import "time"

// Error is a router-level validation or state-machine violation.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

// TaskSpec is the Master-facing dispatch input (subset for the Go port scaffold).
type TaskSpec struct {
	TaskID        string
	Goal          string
	CallbackTopic string
}

// Task is a persisted Hub row (subset for the Go port scaffold).
type Task struct {
	TaskID        string
	Goal          string
	CallbackTopic string
	Status        string
	Attempt       int
	WorkerID      string
	ClaimToken    string
	CreatedAt     time.Time
	CompletedAt   time.Time
	Summary       string
}

// DispatchResponse mirrors the gRPC dispatch ACK fields used by conformance tests.
type DispatchResponse struct {
	TaskID        string
	CallbackTopic string
	Status        string
	IdempotentHit bool
	Attempt       int
}
