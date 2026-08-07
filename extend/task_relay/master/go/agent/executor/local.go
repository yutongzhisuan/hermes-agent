package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

type LocalOptions struct {
	Shell string
}

type localBackend struct {
	shell     string
	bwrapPath string
}

func NewLocal(opts LocalOptions) (Executor, error) {
	shell := opts.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	if _, err := exec.LookPath(shell); err != nil {
		return nil, fmt.Errorf("shell %q: %w", shell, err)
	}
	bwrapPath, _ := exec.LookPath("bwrap")
	return &localBackend{shell: shell, bwrapPath: bwrapPath}, nil
}

func (l *localBackend) Name() string { return "local" }

func (l *localBackend) Sandboxed() bool { return l.bwrapPath != "" }

func (l *localBackend) Run(ctx context.Context, spec Spec) (JobResult, error) {
	res := JobResult{Backend: l.Name(), StartedAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	args := []string{"-c", spec.Command}
	bin := l.shell
	if l.bwrapPath != "" {
		bwrapArgs := []string{
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--proc", "/proc",
			"--unshare-pid",
			"--unshare-ipc",
			"--cap-drop", "ALL",
			"--die-with-parent",
		}
		if spec.WorkDir != "" {
			bwrapArgs = append(bwrapArgs, "--bind", spec.WorkDir, spec.WorkDir, "--chdir", spec.WorkDir)
		}
		bwrapArgs = append(bwrapArgs, "--", l.shell, "-c", spec.Command)
		bin = l.bwrapPath
		args = bwrapArgs
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if l.bwrapPath == "" && spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = mergeEnv(spec.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, max: spec.MaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, max: spec.MaxOutputBytes}

	err := cmd.Run()
	res.FinishedAt = time.Now()
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if ctx.Err() == context.Canceled {
		res.Canceled = true
		res.ExitCode = -1
		return res, nil
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("run: %w", err)
	}
	return res, nil
}

func mergeEnv(overrides map[string]string) []string {
	base := map[string]string{}
	for _, kv := range syscall.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				base[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	for k, v := range overrides {
		base[k] = v
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}

type limitedWriter struct {
	w   *bytes.Buffer
	max int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	remaining := l.max - int64(l.w.Len())
	if remaining > 0 {
		if int64(len(p)) > remaining {
			l.w.Write(p[:remaining])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
