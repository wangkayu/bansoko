package application

import (
	"context"
	"database/sql"

	animeapplication "bansoko/internal/anime/application"
)

// namingRecoveryChange projects the owner-input change onto the shared
// recovery scope.
func (c metadataChange) namingRecoveryChange() animeapplication.NamingRecoveryChange {
	return animeapplication.NamingRecoveryChange{
		Title:     c.title,
		MediaKind: c.mediaKind,
		Year:      c.year,
		Season:    c.season,
	}
}

// scheduleNamingRecoveryReleaseEvaluation re-evaluates only failed acquisitions
// whose persisted reason names a fact this metadata change supplied. The
// generic release scope still excludes acquisition_failed; this separate scope
// also requires no download task and relies on the existing work-item merge for
// idempotency.
func (s *Service) scheduleNamingRecoveryReleaseEvaluation(ctx context.Context, tx *sql.Tx, managedAnimeID string, change metadataChange) (int, error) {
	return animeapplication.ScheduleNamingRecoveryTx(ctx, tx, s.workItems, managedAnimeID, change.namingRecoveryChange(), "metadata_naming_recovery")
}
