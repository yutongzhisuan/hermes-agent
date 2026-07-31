package router

import (
	"context"
	"time"
)

const pruneInterval = time.Hour

// PruneEvents deletes task events older than RetentionDays.
func (r *Router) PruneEvents(ctx context.Context) error {
	if r.cfg.RetentionDays <= 0 {
		return nil
	}
	cutoff := r.now().Add(-time.Duration(r.cfg.RetentionDays) * 24 * time.Hour)
	_, err := r.store.PruneEventsBefore(ctx, cutoff)
	return err
}

// MaybePruneEvents runs PruneEvents at most once per pruneInterval.
func (r *Router) MaybePruneEvents(ctx context.Context) error {
	now := r.now()
	if !r.lastPruneAt.IsZero() && now.Sub(r.lastPruneAt) < pruneInterval {
		return nil
	}
	r.lastPruneAt = now
	return r.PruneEvents(ctx)
}
