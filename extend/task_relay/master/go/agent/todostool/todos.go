package todostool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

var validStatuses = map[string]bool{
	"pending":     true,
	"in_progress": true,
	"completed":   true,
}

type TodoItem struct {
	ID      string `json:"id" jsonschema:"description=Stable item id; empty = auto-generated"`
	Content string `json:"content" jsonschema:"required,description=Task description"`
	Status  string `json:"status" jsonschema:"required,description=pending|in_progress|completed"`
}

type TodosInput struct {
	Items []TodoItem `json:"items" jsonschema:"required,description=Full replacement todo list"`
}

type TodosOutput struct {
	Items []TodoItem `json:"items"`
}

type TodosTool struct {
	path string
}

func NewTodosTool(path string) *TodosTool {
	return &TodosTool{path: path}
}

func (t *TodosTool) Run(ctx context.Context, in TodosInput) (TodosOutput, error) {
	items := make([]TodoItem, len(in.Items))
	for i, item := range in.Items {
		if !validStatuses[item.Status] {
			return TodosOutput{}, fmt.Errorf("invalid status %q: must be pending, in_progress, or completed", item.Status)
		}
		if item.Content == "" {
			return TodosOutput{}, fmt.Errorf("item %d: content must not be empty", i)
		}
		if item.ID == "" {
			item.ID = uuid.NewString()[:8]
		}
		items[i] = item
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return TodosOutput{}, fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return TodosOutput{}, fmt.Errorf("marshal: %w", err)
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return TodosOutput{}, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, t.path); err != nil {
		return TodosOutput{}, fmt.Errorf("rename: %w", err)
	}
	return TodosOutput{Items: items}, nil
}

func (t *TodosTool) Load() ([]TodoItem, error) {
	data, err := os.ReadFile(t.path)
	if os.IsNotExist(err) {
		return []TodoItem{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var items []TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse %s: %w", t.path, err)
	}
	return items, nil
}
