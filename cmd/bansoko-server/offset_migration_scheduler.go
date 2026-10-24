package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bansoko/internal/filemaintenance"
	observabilityport "bansoko/internal/observability/port"
)

// runOffsetMigrationPlannerScheduler drives the durable planning phase: every
// admitted pending/planning generation is expanded into its per-placement
// operations (and execute work items) on a short interval. PlanGeneration is
// idempotent, so a crash between admission and planning converges on the next
// tick; planning never performs a filesystem effect.
func runOffsetMigrationPlannerScheduler(ctx context.Context, db *sql.DB, planner *filemaintenance.OffsetMigrationService, logger observabilityport.RuntimeLogger) {
	plan := func() {
		rows, err := db.QueryContext(ctx, `
SELECT id FROM offset_migration_generations
WHERE state IN ('pending', 'planning')
ORDER BY created_at, id
LIMIT 50`)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn(ctx, observabilityport.RuntimeEvent{
					Message: "offset migration planner scan failed", Component: "offset_migration",
					ReasonCode: "offset_planner_scan_failed", ErrorClass: "db",
				})
			}
			return
		}
		defer rows.Close()
		planned := 0
		failed := 0
		for rows.Next() {
			var generationID string
			if err := rows.Scan(&generationID); err != nil {
				logger.Warn(ctx, observabilityport.RuntimeEvent{
					Message: "offset migration planner scan row failed", Component: "offset_migration",
					ReasonCode: "offset_planner_scan_row_failed", ErrorClass: "db",
				})
				return
			}
			if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
				if ctx.Err() == nil {
					logger.Warn(ctx, observabilityport.RuntimeEvent{
						Message:   fmt.Sprintf("offset migration planning failed for %s: %v", generationID, err),
						Component: "offset_migration", ReasonCode: "offset_planner_failed",
					})
				}
				failed++
				continue
			}
			planned++
		}
		if err := rows.Err(); err != nil && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message: "offset migration planner scan iteration failed", Component: "offset_migration",
				ReasonCode: "offset_planner_scan_iter_failed", ErrorClass: "db",
			})
		}
		if planned > 0 && ctx.Err() == nil {
			logger.Info(ctx, observabilityport.RuntimeEvent{
				Message:   fmt.Sprintf("offset migration planner expanded %d generation(s)", planned),
				Component: "offset_migration", ReasonCode: "offset_planner_expanded",
			})
		}
		if failed > 0 && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:   fmt.Sprintf("offset migration planner skipped %d generation(s)", failed),
				Component: "offset_migration", ReasonCode: "offset_planner_skipped",
			})
		}
	}

	plan()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			plan()
		}
	}
}
