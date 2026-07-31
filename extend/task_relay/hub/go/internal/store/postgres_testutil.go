package store

import "context"

// TruncatePostgresTables removes all rows from Hub tables (integration tests only).
func TruncatePostgresTables(ctx context.Context, st interface{ Close() error }) error {
	pg, ok := st.(*Postgres)
	if !ok {
		return nil
	}
	_, err := pg.db.ExecContext(ctx,
		`TRUNCATE checkpoints, task_events, tasks, workers, batches, audit_log RESTART IDENTITY CASCADE`,
	)
	return err
}
