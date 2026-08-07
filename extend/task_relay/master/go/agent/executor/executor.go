package executor

import (
	"context"
	"time"
)

type Spec struct {
	Command        string
	WorkDir        string
	Timeout        time.Duration
	Env            map[string]string
	Backend        string
	MaxOutputBytes int64
}

func (s Spec) WithDefaults(defaultTimeout, maxTimeout time.Duration, maxOutput int64) Spec {
	if s.Timeout <= 0 {
		s.Timeout = defaultTimeout
	}
	if s.Timeout > maxTimeout {
		s.Timeout = maxTimeout
	}
	if s.MaxOutputBytes <= 0 {
		s.MaxOutputBytes = maxOutput
	}
	if s.Backend == "" {
		s.Backend = "local"
	}
	return s
}

type JobResult struct {
	ExitCode           int
	Stdout, Stderr     string
	TimedOut, Canceled bool
	Backend            string
	StartedAt          time.Time
	FinishedAt         time.Time
}

type Executor interface {
	Run(ctx context.Context, spec Spec) (JobResult, error)
	Name() string
}
