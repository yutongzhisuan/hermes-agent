package router

import "time"

// RouterConfig holds timeout and scheduling defaults (aligned with Python HubConfig M1).
type RouterConfig struct {
	QueueTimeoutSeconds          int
	FirstProgressSeconds         int
	TimeoutSeconds               int
	CancelGraceSeconds           int
	MaxAttempts                  int
	WorkerStaleSeconds           int
	PollOfferSeconds             int
	TickInterval                 time.Duration
	JWTSecret                    string
	EncryptInlineContextAtRest   bool
	RequireSignedContextRef      bool
}

// DefaultRouterConfig returns design-spec defaults for the Go Hub port.
func DefaultRouterConfig() RouterConfig {
	return RouterConfig{
		QueueTimeoutSeconds:  900,
		FirstProgressSeconds: 120,
		TimeoutSeconds:       600,
		CancelGraceSeconds:   60,
		MaxAttempts:          1,
		WorkerStaleSeconds:   90,
		PollOfferSeconds:     30,
		TickInterval:         time.Second,
	}
}

func (c RouterConfig) queueTimeout(task *Task) int {
	if task.QueueTimeoutSeconds > 0 {
		return task.QueueTimeoutSeconds
	}
	return c.QueueTimeoutSeconds
}

func (c RouterConfig) firstProgress(task *Task) int {
	if task.FirstProgressSeconds > 0 {
		return task.FirstProgressSeconds
	}
	return c.FirstProgressSeconds
}

func (c RouterConfig) executionTimeout(task *Task) int {
	if task.TimeoutSeconds > 0 {
		return task.TimeoutSeconds
	}
	return c.TimeoutSeconds
}
