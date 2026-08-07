package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/infa/task_relay/master/agent/policy"
)

const defaultTimeoutSeconds = 5

type Hook struct {
	Command        string `json:"command" yaml:"command"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`
}

type Runner struct {
	Hooks   []Hook
	Audit   *policy.AuditLogger
	Session string
}

type payload struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

func (r *Runner) Check(ctx context.Context, toolName, rawArgs string) error {
	if rawArgs == "" {
		rawArgs = "{}"
	}
	stdin, err := json.Marshal(payload{Tool: toolName, Args: json.RawMessage(rawArgs)})
	if err != nil {
		return fmt.Errorf("hook payload: %w", err)
	}
	for _, h := range r.Hooks {
		if err := r.runHook(ctx, h, stdin); err != nil {
			if r.Audit != nil {
				aerr := r.Audit.Log(policy.AuditEntry{
					Operation: "hook_block",
					Command:   toolName,
					Decision:  "deny",
					ExitCode:  -1,
					Error:     err.Error(),
					Session:   r.Session,
				})
				if aerr != nil {
					return fmt.Errorf("%w (audit: %v)", err, aerr)
				}
			}
			return err
		}
	}
	return nil
}

func (r *Runner) runHook(ctx context.Context, h Hook, stdin []byte) error {
	timeout := defaultTimeoutSeconds * time.Second
	if h.TimeoutSeconds > 0 {
		timeout = time.Duration(h.TimeoutSeconds) * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(hctx, h.Command)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if hctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("blocked by hook: hook %q timed out after %s", h.Command, timeout)
	}
	if _, ok := err.(*exec.ExitError); ok {
		reason := strings.TrimSpace(stdout.String())
		if reason == "" {
			reason = err.Error()
		}
		return fmt.Errorf("blocked by hook: %s", reason)
	}
	return fmt.Errorf("blocked by hook: failed to run %q: %v", h.Command, err)
}

func (r *Runner) Wrap(t tool.InvokableTool) tool.InvokableTool {
	return &hookedTool{inner: t, runner: r}
}

func (r *Runner) WrapAll(tools []tool.BaseTool) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(tools))
	for _, t := range tools {
		if it, ok := t.(tool.InvokableTool); ok {
			out = append(out, r.Wrap(it))
		} else {
			out = append(out, t)
		}
	}
	return out
}

type hookedTool struct {
	inner  tool.InvokableTool
	runner *Runner
	name   string
}

func (h *hookedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return h.inner.Info(ctx)
}

func (h *hookedTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	name := h.name
	if name == "" {
		info, err := h.inner.Info(ctx)
		if err != nil {
			return "", fmt.Errorf("hook resolve tool info: %w", err)
		}
		name = info.Name
		h.name = name
	}
	if err := h.runner.Check(ctx, name, argumentsInJSON); err != nil {
		return "", err
	}
	return h.inner.InvokableRun(ctx, argumentsInJSON, opts...)
}
