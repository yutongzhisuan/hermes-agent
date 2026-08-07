package webtools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/infa/task_relay/master/agent/policy"
)

type DownloadTool struct {
	deps *Deps
}

func NewDownloadTool(deps *Deps) *DownloadTool { return &DownloadTool{deps: deps} }

type DownloadInput struct {
	URL  string `json:"url" jsonschema:"required,description=HTTP(S) URL to download"`
	Path string `json:"path" jsonschema:"required,description=Destination file path, relative to the configured root or absolute (policy-gated)"`
}

type DownloadOutput struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytes_written"`
	StatusCode   int    `json:"status_code"`
	Truncated    bool   `json:"truncated"`
}

func (t *DownloadTool) Run(ctx context.Context, in DownloadInput) (DownloadOutput, error) {
	target := in.URL + " → " + in.Path

	u, err := t.deps.validateURL(in.URL)
	if err != nil {
		if auditErr := t.deps.auditDenied("web_download", in.URL, policy.Deny.String()); auditErr != nil {
			return DownloadOutput{}, auditErr
		}
		return DownloadOutput{}, err
	}

	if decision := t.deps.Paths.EvaluatePath(in.Path); decision != policy.Allow {
		if auditErr := t.deps.auditDenied("web_download", target, decision.String()); auditErr != nil {
			return DownloadOutput{}, auditErr
		}
		return DownloadOutput{}, fmt.Errorf("web_download denied by policy: %s", in.Path)
	}

	client := &http.Client{
		Transport: secureTransport(t.deps.AllowPrivateNetworks),
		Timeout:   t.deps.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if _, err := t.deps.validateURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return DownloadOutput{}, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = t.deps.auditOp("web_download", target, "", -1, err)
		return DownloadOutput{}, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("download %s: unexpected status %d", in.URL, resp.StatusCode)
		_ = t.deps.auditOp("web_download", target, "", resp.StatusCode, err)
		return DownloadOutput{}, err
	}

	abs := resolveIn(t.deps.Paths.Root(), in.Path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		_ = t.deps.auditOp("web_download", target, "", -1, err)
		return DownloadOutput{}, fmt.Errorf("create destination dir: %w", err)
	}

	tmp := fmt.Sprintf("%s.tmp-%d", abs, os.Getpid())
	n, truncated, err := t.streamToFile(tmp, resp.Body)
	if err != nil {
		_ = os.Remove(tmp)
		_ = t.deps.auditOp("web_download", target, "", -1, err)
		return DownloadOutput{}, err
	}

	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		_ = t.deps.auditOp("web_download", target, "", -1, err)
		return DownloadOutput{}, fmt.Errorf("rename into place: %w", err)
	}

	out := DownloadOutput{
		Path:         abs,
		BytesWritten: n,
		StatusCode:   resp.StatusCode,
		Truncated:    truncated,
	}
	summary := fmt.Sprintf("bytes=%d status=%d", n, resp.StatusCode)
	if err := t.deps.auditOp("web_download", target, summary, 0, nil); err != nil {
		return DownloadOutput{}, err
	}
	return out, nil
}

// streamToFile writes at most MaxBytes+1 bytes of body to path. When the
// body exceeded MaxBytes the file is cut to MaxBytes and truncated is true;
// the prefix is kept as a partial artifact.
func (t *DownloadTool) streamToFile(path string, body io.Reader) (int64, bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, false, fmt.Errorf("create temp file: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(body, t.deps.MaxBytes+1))
	if err != nil {
		_ = f.Close()
		return 0, false, fmt.Errorf("write temp file: %w", err)
	}
	if n > t.deps.MaxBytes {
		if err := f.Truncate(t.deps.MaxBytes); err != nil {
			_ = f.Close()
			return 0, false, fmt.Errorf("truncate temp file: %w", err)
		}
		n = t.deps.MaxBytes
		if err := f.Close(); err != nil {
			return 0, false, fmt.Errorf("close temp file: %w", err)
		}
		return n, true, nil
	}
	if err := f.Close(); err != nil {
		return 0, false, fmt.Errorf("close temp file: %w", err)
	}
	return n, false, nil
}
