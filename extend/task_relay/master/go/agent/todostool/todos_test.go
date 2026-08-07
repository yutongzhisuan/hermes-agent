package todostool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTodosSetAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "todos.json")
	tool := NewTodosTool(path)

	in := TodosInput{Items: []TodoItem{
		{Content: "first", Status: "pending"},
		{Content: "second", Status: "in_progress"},
		{Content: "third", Status: "completed"},
	}}
	out, err := tool.Run(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, out.Items, 3)
	for _, item := range out.Items {
		assert.NotEmpty(t, item.ID)
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var onDisk []TodoItem
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	assert.Equal(t, out.Items, onDisk)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTodosInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	tool := NewTodosTool(path)

	_, err := tool.Run(context.Background(), TodosInput{Items: []TodoItem{
		{Content: "ok", Status: "pending"},
		{Content: "bad", Status: "doing"},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "doing")

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr))
}

func TestTodosEmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	tool := NewTodosTool(path)

	_, err := tool.Run(context.Background(), TodosInput{Items: []TodoItem{
		{Content: "", Status: "pending"},
	}})
	require.Error(t, err)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr))
}

func TestTodosAutoID(t *testing.T) {
	dir := t.TempDir()
	tool := NewTodosTool(filepath.Join(dir, "todos.json"))

	out, err := tool.Run(context.Background(), TodosInput{Items: []TodoItem{
		{Content: "a", Status: "pending"},
		{Content: "b", Status: "pending"},
		{ID: "keepme", Content: "c", Status: "pending"},
	}})
	require.NoError(t, err)
	require.Len(t, out.Items, 3)

	seen := map[string]bool{}
	for _, item := range out.Items {
		assert.NotEmpty(t, item.ID)
		assert.False(t, seen[item.ID], "duplicate id %q", item.ID)
		seen[item.ID] = true
	}
	assert.Equal(t, "keepme", out.Items[2].ID)
}

func TestTodosReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")

	first := NewTodosTool(path)
	out, err := first.Run(context.Background(), TodosInput{Items: []TodoItem{
		{Content: "persist me", Status: "in_progress"},
	}})
	require.NoError(t, err)

	second := NewTodosTool(path)
	loaded, err := second.Load()
	require.NoError(t, err)
	assert.Equal(t, out.Items, loaded)
}

func TestTodosLoadMissing(t *testing.T) {
	tool := NewTodosTool(filepath.Join(t.TempDir(), "nope", "todos.json"))
	items, err := tool.Load()
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestTodosAtomicNoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	tool := NewTodosTool(path)

	_, err := tool.Run(context.Background(), TodosInput{Items: []TodoItem{
		{Content: "x", Status: "pending"},
	}})
	require.NoError(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, "todos.json.tmp", e.Name())
	}
	assert.Len(t, entries, 1)
}
