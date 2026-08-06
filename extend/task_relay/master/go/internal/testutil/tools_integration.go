//go:build integration

package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/require"
)

// BuildToolMap indexes invokable tools by name.
func BuildToolMap(t *testing.T, tools []tool.BaseTool) map[string]tool.InvokableTool {
	t.Helper()
	out := make(map[string]tool.InvokableTool, len(tools))
	for _, base := range tools {
		it, ok := base.(tool.InvokableTool)
		require.Truef(t, ok, "tool %T is not invokable", base)
		info, err := base.Info(context.Background())
		require.NoError(t, err)
		out[info.Name] = it
	}
	return out
}

// InvokeTool runs one Eino invokable tool and decodes JSON output.
func InvokeTool[T any](ctx context.Context, t *testing.T, it tool.InvokableTool, in any) T {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	outRaw, err := it.InvokableRun(ctx, string(raw))
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal([]byte(outRaw), &out), "raw=%s", outRaw)
	return out
}

// FindInvokableTool returns a tool by name.
func FindInvokableTool(tools []tool.BaseTool, name string) (tool.InvokableTool, error) {
	for _, base := range tools {
		it, ok := base.(tool.InvokableTool)
		if !ok {
			continue
		}
		info, err := base.Info(context.Background())
		if err != nil {
			return nil, err
		}
		if info.Name == name {
			return it, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found", name)
}
