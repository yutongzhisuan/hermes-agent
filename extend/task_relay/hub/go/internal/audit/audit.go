package audit

import (
	"context"
	"encoding/json"
)

// Writer persists audit rows.
type Writer interface {
	InsertAuditLog(ctx context.Context, action, taskID, masterSessionID, payloadJSON string) error
}

// DispatchACLInput carries task ACL fields for audit logging.
type DispatchACLInput struct {
	TaskID               string
	TargetWorker         string
	AllowedWorkerIDsJSON string
	DenyWorkerIDsJSON    string
}

// RecordDispatchACL persists an audit row when dispatch carries worker ACL constraints.
func RecordDispatchACL(
	ctx context.Context,
	store Writer,
	input DispatchACLInput,
	masterSessionID string,
) error {
	payload := aclPayload(input)
	if payload == nil || store == nil {
		return nil
	}
	payload["master_session_id"] = masterSessionID
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return store.InsertAuditLog(ctx, "dispatch_acl", input.TaskID, masterSessionID, string(raw))
}

func aclPayload(input DispatchACLInput) map[string]any {
	payload := map[string]any{}
	if input.TargetWorker != "" {
		payload["target_worker"] = input.TargetWorker
	}
	if allowed := decodeStringList(input.AllowedWorkerIDsJSON); len(allowed) > 0 {
		payload["allowed_worker_ids"] = allowed
	}
	if denied := decodeStringList(input.DenyWorkerIDsJSON); len(denied) > 0 {
		payload["deny_worker_ids"] = denied
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func decodeStringList(raw string) []string {
	if raw == "" {
		return nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}
