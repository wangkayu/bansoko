package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// Retirement disposition: a running root of a retired
// owner with nothing in flight (no plan, or a queued unsealed plan) no longer
// keeps local cleanup refused. Cleanup settles it to cancelled / OWNER_RETIRED,
// compacts it (owner_cleanup) and deletes the management graph; a retry with
// the same key replays. The refusal while an admitted effect is unresolved is
// TestCleanupAnimeLocalDataProceedsAfterRetirementMidExternalEffect.
func TestLocalCleanupSettlesRunningAcquisitionWithoutLiveWork(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "no_plan"
		if queued {
			name = "queued_plan"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := animetest.OpenTestDB(t)
			owner := disposabledb.Owner(t)
			service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
			sub := subscribeCustomDraft(t, service, "retained-cleanup-key", "https://feeds.example.com/retained.xml", "retained")
			tx, err := owner.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('retained-release',$1,'retained','{}','{}','discovered','new')`, sub.RssSources[0].Id); err != nil {
				t.Fatal(err)
			}
			acquisitionfixture.SeedObservedDownload(t, tx, "retained-root", "retained-task", "retained-release", "retained-task-key", `{}`, `{}`, "queued", `null`)
			if queued {
				if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at) VALUES('retained-artifact','retained-task','retained-root','{}','{}','verified',CURRENT_TIMESTAMP); INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,state,input_digest) SELECT 'retained-plan','retained-artifact',id,1,'queued',admission_digest FROM acquisitions WHERE id='retained-root'`); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := service.DeleteAnimeSubscription(ctx, sub.ManagedAnime.Id, SubscriptionDeleteInput{ExpectedVersion: sub.Subscription.Version}); err != nil {
				t.Fatal(err)
			}
			_, epoch := lifecycleForTest(t, db, sub.ManagedAnime.Id)
			for n := 0; n < 2; n++ {
				if err := service.CleanupAnimeLocalData(ctx, sub.ManagedAnime.Id, localCleanupInput(epoch), "retained-cleanup-request"); err != nil {
					t.Fatalf("cleanup attempt %d: %v", n, err)
				}
			}
			assertCleanupRootSettledAndCompacted(t, db, "retained-root", "cancelled")
			var left int
			if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM download_tasks WHERE id='retained-task')+(SELECT COUNT(*) FROM releases WHERE id='retained-release')+(SELECT COUNT(*) FROM postprocessing_plans WHERE acquisition_id='retained-root')+(SELECT COUNT(*) FROM artifacts WHERE acquisition_id='retained-root')`).Scan(&left); err != nil || left != 0 {
				t.Fatalf("root graph survived cleanup: %d %v", left, err)
			}
		})
	}
}
