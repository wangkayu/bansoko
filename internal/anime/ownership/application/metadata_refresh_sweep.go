package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	animedomain "bansoko/internal/anime/domain"
	observabilityport "bansoko/internal/observability/port"
)

// The sweep is a background freshness pass, not an operation: it re-pulls the
// bound works of active subscriptions on a fixed cadence and never reports
// back to a user action. The cadence is a named constant because there is no
// tuning environment variable (env-surface policy).

const (
	// MetadataRefreshInterval is the fixed period between two sweeps. A restart
	// does not run one at boot; the first pass is one interval after start.
	MetadataRefreshInterval = 24 * time.Hour
	// MetadataRefreshWorkTimeout bounds one work's re-pull so a hung provider
	// connection cannot stall the rest of the pass.
	MetadataRefreshWorkTimeout = 30 * time.Second
)

// MetadataRefreshSweepSource is the sweep's read side and the existing
// per-anime re-pull. *Service implements it: the two reads resolve the Global
// toggle and the active subscriptions' bound works, and the re-pull is the
// same use case the 刷新元数据 action drives.
type MetadataRefreshSweepSource interface {
	MetadataRefreshEnabled(ctx context.Context) (bool, error)
	MetadataRefreshTargets(ctx context.Context) ([]string, error)
	RefreshAnimeMetadataApplication(ctx context.Context, managedAnimeID string) (AnimeMetadata, error)
}

// MetadataRefreshSweepResult is one pass's bounded summary. Enabled reports
// whether the toggle let the pass list and refresh anything.
type MetadataRefreshSweepResult struct {
	Enabled   bool
	Refreshed int
	Failed    int
}

// MetadataRefreshSweep refreshes the bound works of the active subscriptions
// once per pass. A disabled toggle is a silent no-op; a failed work is one
// bounded diagnostic and the next work still runs; the pass itself never
// fails because one provider is unavailable.
type MetadataRefreshSweep struct {
	Source      MetadataRefreshSweepSource
	Logger      observabilityport.RuntimeLogger
	WorkTimeout time.Duration
}

// NewMetadataRefreshSweep wires the sweep to the ownership service and the
// composition root's single runtime logger.
func NewMetadataRefreshSweep(source MetadataRefreshSweepSource, logger observabilityport.RuntimeLogger) *MetadataRefreshSweep {
	return &MetadataRefreshSweep{Source: source, Logger: logger, WorkTimeout: MetadataRefreshWorkTimeout}
}

// RunPass runs one sweep. The toggle is re-read on every pass, so flipping it
// off stops the next pass without a restart.
func (s *MetadataRefreshSweep) RunPass(ctx context.Context) (MetadataRefreshSweepResult, error) {
	if s == nil || s.Source == nil {
		return MetadataRefreshSweepResult{}, fmt.Errorf("metadata refresh sweep is not configured")
	}
	enabled, err := s.Source.MetadataRefreshEnabled(ctx)
	if err != nil {
		return MetadataRefreshSweepResult{}, fmt.Errorf("read metadata_refresh setting: %w", err)
	}
	if !enabled {
		return MetadataRefreshSweepResult{}, nil
	}
	result := MetadataRefreshSweepResult{Enabled: true}
	targets, err := s.Source.MetadataRefreshTargets(ctx)
	if err != nil {
		return result, fmt.Errorf("list metadata refresh targets: %w", err)
	}
	timeout := s.WorkTimeout
	if timeout <= 0 {
		timeout = MetadataRefreshWorkTimeout
	}
	for _, managedAnimeID := range targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// One work's timeout is cancelled as soon as its re-pull returns, so a
		// slow work cannot accumulate timers across a long pass.
		workCtx, cancel := context.WithTimeout(ctx, timeout)
		_, refreshErr := s.Source.RefreshAnimeMetadataApplication(workCtx, managedAnimeID)
		cancel()
		if refreshErr != nil {
			// Shutdown is not a provider failure: stop without a diagnostic
			// instead of blaming the work whose context the stop cancelled.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.Failed++
			if s.Logger != nil {
				// Bounded: a fixed message plus the owning id, never the
				// provider error text (it can carry endpoints and credentials).
				s.Logger.Warn(ctx, observabilityport.RuntimeEvent{
					Message:    "metadata refresh failed for one bound work; the next cycle retries",
					Component:  "metadata_refresh",
					Operation:  "sweep",
					Outcome:    "failure",
					ReasonCode: "metadata_refresh_failed",
					ErrorClass: "metadata",
					OwnerType:  "managed_anime",
					OwnerID:    managedAnimeID,
				})
			}
			continue
		}
		result.Refreshed++
	}
	return result, nil
}

// MetadataRefreshEnabled reads the Global metadata_refresh toggle. A missing
// row is the code default (off); an unreadable or unversioned saved row fails
// closed instead of guessing whether the sweep may run.
func (s *Service) MetadataRefreshEnabled(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("ownership service is not configured")
	}
	var schema string
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT schema_version, value FROM global_config_sections WHERE section_name = $1`, animedomain.SectionMetadataRefresh).Scan(&schema, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return animedomain.DefaultMetadataRefreshRules().Enabled, nil
	}
	if err != nil {
		return false, fmt.Errorf("read metadata_refresh setting: %w", err)
	}
	if schema != animedomain.MetadataRefreshSchemaVersion {
		return false, fmt.Errorf("metadata_refresh: unsupported schema version %q", schema)
	}
	rules, err := animedomain.DecodeMetadataRefresh(raw)
	if err != nil {
		return false, fmt.Errorf("metadata_refresh: %w", err)
	}
	return rules.Enabled, nil
}

// MetadataRefreshTargets lists the bound works of the active subscriptions.
// Stopped subscriptions are never touched, and DISTINCT pins the at-most-once
// rule even though subscriptions.managed_anime_id is already unique.
func (s *Service) MetadataRefreshTargets(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("ownership service is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT sub.managed_anime_id
FROM subscriptions sub
JOIN anime_metadata m ON m.managed_anime_id = sub.managed_anime_id
WHERE sub.state = 'active'
  AND (m.bgm_work_id IS NOT NULL OR m.tmdb_work_id IS NOT NULL)
ORDER BY sub.managed_anime_id`)
	if err != nil {
		return nil, fmt.Errorf("list metadata refresh targets: %w", err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var managedAnimeID string
		if err := rows.Scan(&managedAnimeID); err != nil {
			return nil, fmt.Errorf("scan metadata refresh target: %w", err)
		}
		targets = append(targets, managedAnimeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metadata refresh targets: %w", err)
	}
	return targets, nil
}
