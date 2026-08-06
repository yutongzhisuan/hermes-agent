package store

import (
	"database/sql"
	"time"

	"github.com/infa/task_relay/hub/internal/router"
)

const taskSelectSQL = `
SELECT task_id, batch_id, master_session_id, goal, params_json, context_json, callback_topic,
       status, attempt, max_attempts, worker_id, claim_token, target_worker, toolsets_json,
       depends_on_json, aggregate_key, min_resources_json, trace_context_json,
       allowed_worker_ids_json, deny_worker_ids_json, resume_from_checkpoint, result_json,
       summary, cancel_reason, fields_json, usage_json, error, allow_redispatch, priority,
       queue_timeout_seconds, first_progress_seconds, timeout_seconds, queue_deadline_at,
       first_progress_deadline_at, claim_expires_at, started_at, created_at, completed_at
FROM tasks`

func scanTaskRow(scanner interface {
	Scan(dest ...any) error
}) (*router.Task, error) {
	var batchID sql.NullString
	var masterSessionID sql.NullString
	var paramsJSON sql.NullString
	var contextJSON sql.NullString
	var workerID sql.NullString
	var claimToken sql.NullString
	var targetWorker sql.NullString
	var toolsets sql.NullString
	var dependsOn sql.NullString
	var aggregateKey sql.NullString
	var minResources sql.NullString
	var traceContext sql.NullString
	var allowedWorkers sql.NullString
	var denyWorkers sql.NullString
	var resumeCheckpoint sql.NullString
	var resultJSON sql.NullString
	var summary sql.NullString
	var cancelReason sql.NullString
	var fieldsJSON sql.NullString
	var usageJSON sql.NullString
	var taskError sql.NullString
	var allowRedispatch sql.NullInt64
	var queueTimeout sql.NullInt64
	var firstProgress sql.NullInt64
	var timeoutSeconds sql.NullInt64
	var queueDeadline sql.NullFloat64
	var firstProgressDeadline sql.NullFloat64
	var claimExpires sql.NullFloat64
	var startedAt sql.NullFloat64
	var completedAt sql.NullFloat64
	var createdUnix float64

	task := &router.Task{}
	if err := scanner.Scan(
		&task.TaskID,
		&batchID,
		&masterSessionID,
		&task.Goal,
		&paramsJSON,
		&contextJSON,
		&task.CallbackTopic,
		&task.Status,
		&task.Attempt,
		&task.MaxAttempts,
		&workerID,
		&claimToken,
		&targetWorker,
		&toolsets,
		&dependsOn,
		&aggregateKey,
		&minResources,
		&traceContext,
		&allowedWorkers,
		&denyWorkers,
		&resumeCheckpoint,
		&resultJSON,
		&summary,
		&cancelReason,
		&fieldsJSON,
		&usageJSON,
		&taskError,
		&allowRedispatch,
		&task.Priority,
		&queueTimeout,
		&firstProgress,
		&timeoutSeconds,
		&queueDeadline,
		&firstProgressDeadline,
		&claimExpires,
		&startedAt,
		&createdUnix,
		&completedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	task.BatchID = batchID.String
	task.MasterSessionID = masterSessionID.String
	task.ParamsJSON = paramsJSON.String
	task.ContextJSON = contextJSON.String
	task.WorkerID = workerID.String
	task.ClaimToken = claimToken.String
	task.TargetWorker = targetWorker.String
	task.ToolsetsJSON = toolsets.String
	task.DependsOnJSON = dependsOn.String
	task.AggregateKey = aggregateKey.String
	task.MinResourcesJSON = minResources.String
	task.TraceContextJSON = traceContext.String
	task.AllowedWorkerIDsJSON = allowedWorkers.String
	task.DenyWorkerIDsJSON = denyWorkers.String
	task.ResumeFromCheckpoint = resumeCheckpoint.String
	task.ResultJSON = resultJSON.String
	task.Summary = summary.String
	task.CancelReason = cancelReason.String
	task.FieldsJSON = fieldsJSON.String
	task.UsageJSON = usageJSON.String
	task.Error = taskError.String
	task.AllowRedispatch = allowRedispatch.Int64 != 0
	task.QueueTimeoutSeconds = int(queueTimeout.Int64)
	task.FirstProgressSeconds = int(firstProgress.Int64)
	task.TimeoutSeconds = int(timeoutSeconds.Int64)
	task.CreatedAt = time.Unix(int64(createdUnix), 0)
	if queueDeadline.Valid {
		task.QueueDeadlineAt = time.Unix(int64(queueDeadline.Float64), 0)
	}
	if firstProgressDeadline.Valid {
		task.FirstProgressDeadlineAt = time.Unix(int64(firstProgressDeadline.Float64), 0)
	}
	if claimExpires.Valid {
		task.ClaimExpiresAt = time.Unix(int64(claimExpires.Float64), 0)
	}
	if startedAt.Valid {
		task.StartedAt = time.Unix(int64(startedAt.Float64), 0)
	}
	if completedAt.Valid {
		task.CompletedAt = time.Unix(int64(completedAt.Float64), 0)
	}
	return task, nil
}
