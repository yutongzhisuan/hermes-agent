package filetools

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/infa/task_relay/master/agent/policy"
)

// Deps wires the file tools' collaborators.
type Deps struct {
	Paths         policy.PathEvaluator
	Audit         *policy.AuditLogger
	MaxReadBytes  int64
	MaxWriteBytes int64
	Session       string
}

// checkPath evaluates the path and audits denials. Nil error means allowed.
func (d *Deps) checkPath(op, path string) error {
	decision := d.Paths.EvaluatePath(path)
	if decision == policy.Allow {
		return nil
	}
	entry := policy.AuditEntry{
		Operation: op,
		JobID:     uuid.NewString(),
		Command:   path,
		Decision:  decision.String(),
		ExitCode:  -1,
		Session:   d.Session,
	}
	if err := d.Audit.Log(entry); err != nil {
		return fmt.Errorf("%s denied and audit failed: %w", op, err)
	}
	return fmt.Errorf("%s denied by policy: %s", op, path)
}

// auditOp records a completed operation. Stdout carries the content for hashing.
func (d *Deps) auditOp(op, path string, content string, exitCode int, err error) error {
	entry := policy.AuditEntry{
		Operation: op,
		JobID:     uuid.NewString(),
		Command:   path,
		Backend:   "local",
		Decision:  policy.Allow.String(),
		ExitCode:  exitCode,
		Stdout:    content,
		Session:   d.Session,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if logErr := d.Audit.Log(entry); logErr != nil {
		return fmt.Errorf("audit failed: %w", logErr)
	}
	return nil
}
