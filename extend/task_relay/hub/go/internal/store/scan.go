package store

import (
	"database/sql"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

func scanTaskRow(scanner interface {
	Scan(dest ...any) error
}) (*router.Task, error) {
	var batchID sql.NullString
	var workerID sql.NullString
	var claimToken sql.NullString
	var targetWorker sql.NullString
	var toolsets sql.NullString
	var dependsOn sql.NullString
	var aggregateKey sql.NullString
	var minResources sql.NullString
	var taskError sql.NullString
	var summary sql.NullString
	var cancelReason sql.NullString
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
		&task.Goal,
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
		&taskError,
		&task.Priority,
		&queueTimeout,
		&firstProgress,
		&timeoutSeconds,
		&queueDeadline,
		&firstProgressDeadline,
		&claimExpires,
		&startedAt,
		&summary,
		&cancelReason,
		&createdUnix,
		&completedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	task.BatchID = batchID.String
	task.WorkerID = workerID.String
	task.ClaimToken = claimToken.String
	task.TargetWorker = targetWorker.String
	task.ToolsetsJSON = toolsets.String
	task.DependsOnJSON = dependsOn.String
	task.AggregateKey = aggregateKey.String
	task.MinResourcesJSON = minResources.String
	task.Error = taskError.String
	task.Summary = summary.String
	task.CancelReason = cancelReason.String
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
