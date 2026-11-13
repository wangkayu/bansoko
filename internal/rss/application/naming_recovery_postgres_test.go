package application

import (
	"context"
	"testing"

	animeapplication "bansoko/internal/anime/application"
)

func TestNamingRecoveryReopensFailureAndAutomaticEvaluationDownloads(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-naming-recovery")
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE releases
SET verdict = 'acquisition_failed', reason_code = 'naming_fact_missing_anime_season_folder'
WHERE id = 'release-naming-recovery'`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scheduled, err := animeapplication.ScheduleNamingRecoveryTx(ctx, tx, store, "anime-rss-test", animeapplication.NamingRecoveryChange{Season: true}, "metadata_naming_recovery")
	if err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("scheduled recoveries = %d, want 1", scheduled)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertReleaseState(t, db, "release-naming-recovery", "accepted_pending_download", "accepted", false)

	if ran := evaluateDueReleases(t, store, service); ran != 1 {
		t.Fatalf("automatic evaluations = %d, want 1", ran)
	}
	assertReleaseState(t, db, "release-naming-recovery", "accepted_pending_download", "accepted", true)
}
