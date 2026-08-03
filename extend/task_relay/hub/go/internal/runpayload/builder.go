package runpayload

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/contextcrypto"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/resources"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
)

// Builder assembles worker-facing task.run payloads from Hub task state.
type Builder struct {
	Store         router.Store
	DecryptSecret string
	EncryptAtRest bool
}

// Build constructs the task.run dict for poll, claim, or Mode C push.
func (b *Builder) Build(ctx context.Context, claimed router.ClaimedTask) (map[string]any, error) {
	task, err := b.Store.GetTask(ctx, claimed.TaskID)
	if err != nil || task == nil {
		return map[string]any{}, err
	}
	run := map[string]any{
		"task_id":         task.TaskID,
		"attempt":         claimed.Attempt,
		"goal":            task.Goal,
		"params":          decodeJSON(task.ParamsJSON),
		"context":         decodeContext(task.ContextJSON, b.DecryptSecret, b.EncryptAtRest),
		"toolsets":        decodeStringList(task.ToolsetsJSON),
		"timeout_seconds": claimed.TimeoutSeconds,
		"claim_token":     claimed.ClaimToken,
	}
	if task.FirstProgressSeconds > 0 {
		run["first_progress_seconds"] = task.FirstProgressSeconds
	}
	if trace := decodeJSON(task.TraceContextJSON); trace != nil {
		run["trace_context"] = trace
	}
	if task.ResumeFromCheckpoint != "" {
		run["resume_from_checkpoint"] = task.ResumeFromCheckpoint
		checkpoint, cpErr := b.Store.GetLatestCheckpoint(ctx, task.TaskID)
		if cpErr == nil && checkpoint != nil && len(checkpoint.ResumeBlob) > 0 {
			run["resume_blob"] = base64.StdEncoding.EncodeToString(checkpoint.ResumeBlob)
		}
	}
	return run, nil
}

// BuildPreview constructs metadata-only preview for two-step poll offers.
func BuildPreview(task *router.Task, offered router.OfferedTask) map[string]any {
	if task == nil {
		return map[string]any{}
	}
	excerpt := task.Goal
	if len(excerpt) > 80 {
		excerpt = excerpt[:80]
	}
	preview := map[string]any{
		"goal_excerpt":            excerpt,
		"toolsets":                decodeStringList(task.ToolsetsJSON),
		"priority":                task.Priority,
		"attempt":                 offered.Attempt,
		"timeout_seconds":         offered.TimeoutSeconds,
		"context_bytes":           len(task.ContextJSON),
		"has_resume_checkpoint":   task.ResumeFromCheckpoint != "",
	}
	if req := resources.ParseMinResources(task.MinResourcesJSON); req != nil {
		preview["min_resources"] = req
	}
	return preview
}

func decodeJSON(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	if out == nil {
		return map[string]any{}
	}
	return out
}

func decodeContext(raw, secret string, encryptAtRest bool) any {
	if raw == "" {
		return map[string]any{}
	}
	if encryptAtRest && secret != "" {
		decoded, err := contextcrypto.DecryptContextJSON(raw, secret)
		if err == nil {
			if decoded == nil {
				return map[string]any{}
			}
			return decoded
		}
	}
	return decodeJSON(raw)
}

func decodeStringList(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return []string{}
	}
	return items
}
