package application

import (
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"testing"
	"time"
)

func TestPGReleaseRecoveryDoesNotReadmitOrFetch(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "committed"}[terminal], func(t *testing.T) {
			db := openRSSDatabase(t)
			source, _ := seedRSSOwner(t, db, false, true)
			insertReleaseForEvaluation(t, db, source, "recovery-release", "Test Show 01", "https://downloads.example.test/show.torrent")
			if terminal {
				if _, err := db.Exec(`UPDATE releases SET verdict='adopted_existing' WHERE id='recovery-release'`); err != nil {
					t.Fatal(err)
				}
			}
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			fetcher := &rssFakeSourceFetcher{}
			service, err := newRSSService(t, db, store, fetcher)
			if err != nil {
				t.Fatal(err)
			}
			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "recovery-release"}, workitems.KindReleaseEvaluate, 1)
			if err := store.BlockRecovery(context.Background(), claim, "attempt_exhausted"); err != nil {
				t.Fatal(err)
			}
			if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
				t.Fatal(err)
			}
			c, err := store.ClaimRecovery(context.Background(), []string{workitems.KindReleaseEvaluate}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			h := service.ReleaseEvaluateHandler()
			observer, ok := h.(worker.RecoveryObserver)
			if !ok {
				t.Fatal("production registry handler lacks observer")
			}
			o, err := observer.ObserveRecovery(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if o.Kind != worker.OutcomeFinish {
				t.Fatalf("missing recovery outcome: %+v", o)
			}
			if err := store.SettleRecovery(context.Background(), c, o.TerminalState, o.OwnerResult, o.Disposition, o.WriteOwner, o.PostTransition); err != nil {
				t.Fatal(err)
			}
			var verdict string
			var version int64
			if err := db.QueryRow(`SELECT verdict,version FROM releases WHERE id='recovery-release'`).Scan(&verdict, &version); err != nil {
				t.Fatal(err)
			}
			expected := "acquisition_failed"
			expectedVersion := int64(2)
			if terminal {
				expected = "adopted_existing"
				expectedVersion = 1
			}
			if verdict != expected || version != expectedVersion || fetcher.calls != 0 {
				t.Fatalf("recovery reran admission: verdict=%s version=%d fetches=%d", verdict, version, fetcher.calls)
			}
			var tasks int
			if err := db.QueryRow(`SELECT count(*) FROM download_tasks`).Scan(&tasks); err != nil {
				t.Fatal(err)
			}
			if tasks != 0 {
				t.Fatal("recovery created download")
			}
		})
	}
}
