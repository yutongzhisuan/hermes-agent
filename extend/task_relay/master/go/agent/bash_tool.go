package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/agent/policy"
)

type ExecLimits struct {
	TimeoutDefault time.Duration
	TimeoutMax     time.Duration
	MaxOutputBytes int64
}

type BashToolDeps struct {
	Evaluator    policy.Evaluator
	Executor     executor.Executor
	Audit        *policy.AuditLogger
	Limits       ExecLimits
	EnvAllowKeys []string
	Session      string
}

type BashTool struct {
	deps BashToolDeps
}

func NewBashTool(deps BashToolDeps) *BashTool { return &BashTool{deps: deps} }

type BashInput struct {
	Command        string            `json:"command" jsonschema:"required,description=Shell command to execute"`
	WorkDir        string            `json:"workdir,omitempty" jsonschema:"description=Working directory; defaults to master working dir"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"description=Timeout in seconds; default and max from exec.limits"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"description=Extra env vars; keys outside exec.policy.env_allow_keys are stripped"`
}

type BashOutput struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
}

func (b *BashTool) Run(ctx context.Context, in BashInput) (BashOutput, error) {
	d := b.deps
	spec := executor.Spec{
		Command: in.Command,
		WorkDir: in.WorkDir,
		Env:     filterEnv(in.Env, d.EnvAllowKeys),
	}
	if in.TimeoutSeconds > 0 {
		spec.Timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	spec = spec.WithDefaults(d.Limits.TimeoutDefault, d.Limits.TimeoutMax, d.Limits.MaxOutputBytes)

	decision := d.Evaluator.Evaluate(spec.Command)
	entry := policy.AuditEntry{
		JobID:    uuid.NewString(),
		Command:  spec.Command,
		Decision: decision.String(),
		WorkDir:  spec.WorkDir,
		Session:  d.Session,
	}

	switch decision {
	case policy.Deny:
		return b.deny(entry, "denied by policy")
	case policy.NeedsApproval:
		return b.deny(entry, "needs approval (approval workflow not yet enabled)")
	}

	entry.Backend = d.Executor.Name()
	res, err := d.Executor.Run(ctx, spec)
	if err != nil {
		entry.ExitCode = -1
		entry.Stdout = err.Error()
		if logErr := d.Audit.Log(entry); logErr != nil {
			return BashOutput{}, fmt.Errorf("execution failed (%v) and audit failed: %w", err, logErr)
		}
		return BashOutput{}, err
	}

	entry.ExitCode = res.ExitCode
	entry.DurationMs = res.FinishedAt.Sub(res.StartedAt).Milliseconds()
	entry.Stdout = res.Stdout
	if logErr := d.Audit.Log(entry); logErr != nil {
		return BashOutput{}, fmt.Errorf("audit failed after execution: %w", logErr)
	}
	return BashOutput{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		TimedOut: res.TimedOut,
	}, nil
}

func (b *BashTool) deny(entry policy.AuditEntry, reason string) (BashOutput, error) {
	entry.ExitCode = -1
	if err := b.deps.Audit.Log(entry); err != nil {
		return BashOutput{}, fmt.Errorf("denied and audit failed: %w", err)
	}
	return BashOutput{ExitCode: -1, Stderr: reason}, nil
}

func filterEnv(env map[string]string, allowKeys []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(allowKeys))
	for _, k := range allowKeys {
		allow[k] = struct{}{}
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if _, ok := allow[k]; ok {
			out[k] = v
		}
	}
	return out
}
