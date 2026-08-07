package filetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ViewTool struct {
	deps *Deps
}

func NewViewTool(deps *Deps) *ViewTool { return &ViewTool{deps: deps} }

type ViewInput struct {
	Path   string `json:"path" jsonschema:"required,description=File path, relative to the configured root or absolute"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=1-based starting line; default 1"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Max lines to return; default all (subject to byte cap)"`
}

type ViewOutput struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

func (v *ViewTool) Run(ctx context.Context, in ViewInput) (ViewOutput, error) {
	if err := v.deps.checkPath("file_view", in.Path); err != nil {
		return ViewOutput{}, err
	}
	data, err := os.ReadFile(resolveIn(v.deps.Paths.Root(), in.Path))
	if err != nil {
		_ = v.deps.auditOp("file_view", in.Path, "", -1, err)
		return ViewOutput{}, fmt.Errorf("read: %w", err)
	}
	truncated := false
	if int64(len(data)) > v.deps.MaxReadBytes {
		data = data[:v.deps.MaxReadBytes]
		truncated = true
	}
	lines := strings.Split(string(data), "\n")
	total := len(lines)
	if total > 0 && lines[total-1] == "" {
		total--
	}
	start := 0
	if in.Offset > 1 {
		start = in.Offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if in.Limit > 0 && start+in.Limit < end {
		end = start + in.Limit
	}
	var b strings.Builder
	for i, line := range lines[start:end] {
		fmt.Fprintf(&b, "%d: %s\n", start+i+1, line)
	}
	if truncated {
		b.WriteString("[truncated: byte cap reached]\n")
	}
	out := ViewOutput{Content: b.String(), TotalLines: total, Truncated: truncated}
	if err := v.deps.auditOp("file_view", in.Path, out.Content, 0, nil); err != nil {
		return ViewOutput{}, err
	}
	return out, nil
}

// resolveIn joins relative paths against root; absolute paths pass through
// (the policy evaluator already validated them).
func resolveIn(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}
