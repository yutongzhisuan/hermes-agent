package filetools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type EditTool struct {
	deps *Deps
}

func NewEditTool(deps *Deps) *EditTool { return &EditTool{deps: deps} }

type EditInput struct {
	Path       string `json:"path" jsonschema:"required,description=File to edit"`
	OldString  string `json:"old_string" jsonschema:"required,description=Exact text to replace; must match uniquely unless replace_all"`
	NewString  string `json:"new_string" jsonschema:"required,description=Replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=Replace every occurrence"`
}

type EditOutput struct {
	Replacements int `json:"replacements"`
}

type MultiEditTool struct {
	deps *Deps
}

func NewMultiEditTool(deps *Deps) *MultiEditTool { return &MultiEditTool{deps: deps} }

type EditOp struct {
	OldString string `json:"old_string" jsonschema:"required"`
	NewString string `json:"new_string" jsonschema:"required"`
}

type MultiEditInput struct {
	Path  string   `json:"path" jsonschema:"required,description=File to edit"`
	Edits []EditOp `json:"edits" jsonschema:"required,description=Ordered replacements; each old_string must match uniquely. All-or-nothing."`
}

func (e *EditTool) Run(ctx context.Context, in EditInput) (EditOutput, error) {
	if err := e.deps.checkPath("file_edit", in.Path); err != nil {
		return EditOutput{}, err
	}
	abs := resolveIn(e.deps.Paths.Root(), in.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		_ = e.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("read: %w", err)
	}
	content, n, err := applyEdit(string(data), in.OldString, in.NewString, in.ReplaceAll)
	if err != nil {
		return EditOutput{}, err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		_ = e.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("write: %w", err)
	}
	out := EditOutput{Replacements: n}
	summary := fmt.Sprintf("old=%q new=%q n=%d", in.OldString, in.NewString, n)
	if err := e.deps.auditOp("file_edit", in.Path, summary, 0, nil); err != nil {
		return EditOutput{}, err
	}
	return out, nil
}

func (m *MultiEditTool) Run(ctx context.Context, in MultiEditInput) (EditOutput, error) {
	if err := m.deps.checkPath("file_edit", in.Path); err != nil {
		return EditOutput{}, err
	}
	abs := resolveIn(m.deps.Paths.Root(), in.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		_ = m.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("read: %w", err)
	}
	content := string(data)
	total := 0
	for i, op := range in.Edits {
		next, n, err := applyEdit(content, op.OldString, op.NewString, false)
		if err != nil {
			return EditOutput{}, fmt.Errorf("edit %d: %w (no changes written)", i, err)
		}
		content = next
		total += n
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		_ = m.deps.auditOp("file_edit", in.Path, "", -1, err)
		return EditOutput{}, fmt.Errorf("write: %w", err)
	}
	out := EditOutput{Replacements: total}
	summary := fmt.Sprintf("multiedit n=%d ops=%d", total, len(in.Edits))
	if err := m.deps.auditOp("file_edit", in.Path, summary, 0, nil); err != nil {
		return EditOutput{}, err
	}
	return out, nil
}

func applyEdit(content, oldStr, newStr string, replaceAll bool) (string, int, error) {
	if oldStr == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	n := strings.Count(content, oldStr)
	if n == 0 {
		return "", 0, fmt.Errorf("old_string not found")
	}
	if n > 1 && !replaceAll {
		return "", 0, fmt.Errorf("old_string matches multiple locations (%d); pass replace_all or a longer unique string", n)
	}
	return strings.ReplaceAll(content, oldStr, newStr), n, nil
}
