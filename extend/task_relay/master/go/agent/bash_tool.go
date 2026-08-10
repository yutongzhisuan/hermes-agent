package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/infa/task_relay/master/agent/executor"
	"github.com/infa/task_relay/master/agent/policy"
)

type ExecLimits struct {
	TimeoutDefault time.Duration
	TimeoutMax     time.Duration
	MaxOutputBytes int64
}

type BashToolDeps struct {
	Evaluator      policy.Evaluator
	Executor       executor.Executor
	Remote         executor.Executor // nil = remote unavailable
	DefaultBackend string            // "local" | "remote"; empty = local
	Audit          *policy.AuditLogger
	Approval       policy.ApprovalService // nil = no approval service
	Limits         ExecLimits
	EnvAllowKeys   []string
	Session        string
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
	Backend        string            `json:"backend,omitempty" jsonschema:"description=Execution backend: local or remote; defaults to the configured default_backend"`
}

type BashOutput struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
}

// 已知取舍（首轮）：执行完成后才写审计，进程 crash 存在"执行了但无记录"窗口；
// allow_list 按命令头匹配，复合命令（&&/;）的后续部分不受 allow 限制，由 deny_list 子串兜底。
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
	span := trace.SpanFromContext(ctx)
	entry := policy.AuditEntry{
		Operation: "bash",
		JobID:     uuid.NewString(),
		Command:   spec.Command,
		Decision:  decision.String(),
		WorkDir:   spec.WorkDir,
		Session:   d.Session,
	}

	switch decision {
	case policy.Deny:
		span.SetAttributes(
			attribute.String("exec.backend", "none"),
			attribute.String("exec.decision", decision.String()),
		)
		return b.deny(entry, "denied by policy")
	case policy.NeedsApproval:
		if d.Approval == nil {
			span.SetAttributes(
				attribute.String("exec.backend", "none"),
				attribute.String("exec.decision", decision.String()),
			)
			return b.deny(entry, "needs approval (approval workflow not yet enabled)")
		}
		approved, approvalErr := d.Approval.RequestApproval(ctx, policy.ApprovalRequest{
			JobID:   entry.JobID,
			Command: spec.Command,
			Session: d.Session,
		})
		if approvalErr != nil || !approved {
			reason := "rejected by approval service"
			if approvalErr != nil {
				reason = approvalErr.Error()
			}
			entry.Error = reason
			span.SetAttributes(
				attribute.String("exec.backend", "none"),
				attribute.String("exec.decision", decision.String()),
			)
			return b.deny(entry, reason)
		}
		entry.Decision = "approved"
	}

	execBackend := d.Executor
	backend := in.Backend
	if backend == "" {
		backend = d.DefaultBackend
	}
	switch backend {
	case "", "local":
	case "remote":
		if d.Remote == nil {
			return BashOutput{}, fmt.Errorf("remote backend unavailable (no hub connection configured)")
		}
		if len(spec.Env) > 0 {
			// Env is never forwarded to remote workers (leak vector); fail
			// loudly instead of silently dropping it.
			return BashOutput{}, fmt.Errorf("env is not supported on the remote backend")
		}
		execBackend = d.Remote
	default:
		return BashOutput{}, fmt.Errorf("unknown backend %q (want local|remote)", backend)
	}

	entry.Backend = execBackend.Name()
	span.SetAttributes(
		attribute.String("exec.backend", entry.Backend),
		attribute.String("exec.decision", entry.Decision),
	)
	res, err := execBackend.Run(ctx, spec)
	if err != nil {
		entry.ExitCode = -1
		entry.Error = err.Error()
		span.SetAttributes(attribute.Int("exec.exit_code", -1))
		span.RecordError(err)
		if logErr := d.Audit.Log(entry); logErr != nil {
			return BashOutput{}, fmt.Errorf("execution failed (%v) and audit failed: %w", err, logErr)
		}
		return BashOutput{}, err
	}

	entry.ExitCode = res.ExitCode
	entry.DurationMs = res.FinishedAt.Sub(res.StartedAt).Milliseconds()
	span.SetAttributes(
		attribute.Int("exec.exit_code", res.ExitCode),
		attribute.Int64("exec.duration_ms", entry.DurationMs),
	)
	entry.Stdout = res.Stdout
	entry.Stderr = res.Stderr
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

var dangerousEnvKeys = map[string]struct{}{
	"PATH": {}, "LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "DYLD_INSERT_LIBRARIES": {},
	"DYLD_LIBRARY_PATH": {}, "BASH_ENV": {}, "ENV": {}, "SHELLOPTS": {}, "IFS": {},
	"CDPATH": {}, "GLOBIGNORE": {}, "PROMPT_COMMAND": {},
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
		if _, bad := dangerousEnvKeys[k]; bad {
			continue
		}
		if _, ok := allow[k]; ok {
			out[k] = v
		}
	}
	return out
}
