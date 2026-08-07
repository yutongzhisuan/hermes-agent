package filetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type WriteTool struct {
	deps *Deps
}

func NewWriteTool(deps *Deps) *WriteTool { return &WriteTool{deps: deps} }

type WriteInput struct {
	Path    string `json:"path" jsonschema:"required,description=Target file path"`
	Content string `json:"content" jsonschema:"required,description=Full file content to write"`
}

type WriteOutput struct {
	BytesWritten int  `json:"bytes_written"`
	Created      bool `json:"created"`
}

func (w *WriteTool) Run(ctx context.Context, in WriteInput) (WriteOutput, error) {
	if err := w.deps.checkPath("file_write", in.Path); err != nil {
		return WriteOutput{}, err
	}
	if int64(len(in.Content)) > w.deps.MaxWriteBytes {
		return WriteOutput{}, fmt.Errorf("content exceeds max_write_bytes (%d)", w.deps.MaxWriteBytes)
	}
	abs := resolveIn(w.deps.Paths.Root(), in.Path)
	_, statErr := os.Stat(abs)
	created := os.IsNotExist(statErr)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		_ = w.deps.auditOp("file_write", in.Path, "", -1, err)
		return WriteOutput{}, fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		_ = w.deps.auditOp("file_write", in.Path, "", -1, err)
		return WriteOutput{}, fmt.Errorf("write: %w", err)
	}
	out := WriteOutput{BytesWritten: len(in.Content), Created: created}
	if err := w.deps.auditOp("file_write", in.Path, in.Content, 0, nil); err != nil {
		return WriteOutput{}, err
	}
	return out, nil
}
