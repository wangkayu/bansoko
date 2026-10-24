package application

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// ---------------------------------------------------------------------------
// Whole-transaction lock-set competition for the manual retry.
//
// Two sequences, 50 rounds each, both driven by a HELD ROW LOCK from a second
// connection and verified through pg_blocking_pids -- never by goroutine
// scheduling or wall-clock timing. Any 40P01 or semantic anomaly fails the
// sequence immediately; nothing is retried to reach green.
//
//   seq "prefix-yields": a second connection holds the lifecycle row, which the
//     entry's prescribed marker?->lifecycle prefix takes BLOCKING. The entry
//     must be observed really waiting and must close legitimately once the
//     blocker releases.
//
//   seq "set-exhausts": a second connection holds the owner row, the LAST row of
//     the NOWAIT fixed set. The entry must abort and retry as ONE whole
//     transaction, exhaust into the zero-write 409 MANUAL_RETRY_BUSY, and
//     release every row it took -- proved by the blocker acquiring them after.
// ---------------------------------------------------------------------------

const manualRetryCompetitionRounds = 50

// seedFailedRetryItemOnly creates one failed rss_refresh work item for the
// already-seeded source. The owner chain and the runtime row are established
// once per test, so a round only needs its own failed item.
func seedFailedRetryItemOnly(t *testing.T, db *sql.DB, store *workitems.Store, source string, round int) string {
	t.Helper()
	ctx := context.Background()
	// The store refuses a second LIVE item for one owner and kind, so any item a
	// previous round left available is retired first. The failed item this round
	// retries is the one just created below.
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `SELECT public.work_item_close(id,'failed') FROM work_items WHERE owner_type='rss_source_runtime' AND owner_id=$1 AND state='available'`, source); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: source},
		Kind:                 workitems.KindRSSRefresh,
		IdempotencyKey:       "effect-" + source + "-" + strconv.Itoa(round),
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `SELECT public.work_item_close(id,'failed') FROM work_items WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestManualRetryCompetitionPrefixYields(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-compete-yield"
	ctx := context.Background()
	// The owner chain is shared by every round; only the runtime row and the
	// failed work item are per-round.
	animetest.SeedReleaseOwnerChain(t, db, source)
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES($1)`, source); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < manualRetryCompetitionRounds; round++ {
		// One fresh failed item per round: a successful retry consumes it.
		// One source and one runtime row for the whole sequence; only the failed
		// work item is per-round.
		failedID := seedFailedRetryItemOnly(t, db, store, source, round)
		itemSource := source

		// The lifecycle row is resolved through the work item's own fence.
		var managedAnimeID string
		if err := db.QueryRowContext(ctx, `SELECT managed_anime_id FROM work_item_history WHERE id=$1`, failedID).Scan(&managedAnimeID); err != nil {
			t.Fatal(err)
		}
		if managedAnimeID == "" {
			t.Fatalf("round=%d: the seeded item carries no managed anime fence", round)
		}
		blocker, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var holderPID int
		if err := blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
			t.Fatal(err)
		}
		if _, err := blocker.Exec(`SELECT id FROM managed_anime WHERE id=$1 FOR UPDATE`, managedAnimeID); err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		go func() {
			done <- retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, itemSource, failedID, "retry", "w4-yield-key-"+strconv.Itoa(round))
		}()
		waiting := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := owner.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !waiting {
			blocker.Rollback()
			t.Fatalf("seq=prefix-yields round=%d: no observed lock wait on the lifecycle prefix", round)
		}
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("seq=prefix-yields round=%d: the entry must close once the blocker yields, got %v", round, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("seq=prefix-yields round=%d: the entry never returned", round)
		}
		// Legitimate closure: exactly one AVAILABLE item for this owner other
		// than the failed one just retried. Earlier rounds' replacements are
		// retired by the next round's seed, so they must not be counted.
		var available int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE owner_type='rss_source_runtime' AND owner_id=$1 AND id<>$2 AND state='available'`, itemSource, failedID).Scan(&available); err != nil {
			t.Fatalf("seq=prefix-yields round=%d: %v", round, err)
		}
		if available != 1 {
			t.Fatalf("seq=prefix-yields round=%d: want exactly one available replacement, got %d", round, available)
		}
	}
}

func TestManualRetryCompetitionSetExhausts(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-compete-exhaust"
	ctx := context.Background()
	// The owner chain is shared by every round; only the runtime row and the
	// failed work item are per-round.
	animetest.SeedReleaseOwnerChain(t, db, source)
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES($1)`, source); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < manualRetryCompetitionRounds; round++ {
		// One source and one runtime row for the whole sequence; only the failed
		// work item is per-round.
		failedID := seedFailedRetryItemOnly(t, db, store, source, round)
		itemSource := source

		// Hold the owner row: the LAST row of the NOWAIT fixed set, so the
		// advisory and the lifecycle were already taken when it lands.
		blocker, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocker.Exec(`SELECT rss_source_id FROM rss_source_runtime WHERE rss_source_id=$1 FOR UPDATE`, itemSource); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		err = retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, itemSource, failedID, "retry", "w4-exhaust-key-"+strconv.Itoa(round))
		elapsed := time.Since(start)

		var business *apperrors.Business
		if !errors.As(err, &business) {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: want a typed business error, got %v", round, err)
		}
		if business.Status != 409 || business.Code != "MANUAL_RETRY_BUSY" {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: want 409 MANUAL_RETRY_BUSY, got %d %s", round, business.Status, business.Code)
		}
		if business.Code == "MANAGED_ANIME_RETIREMENT_BUSY" {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: borrowed the retirement reason code", round)
		}
		// Exactly five attempts: four backoff waits of 20/40/80/160ms.
		if elapsed < 300*time.Millisecond || elapsed >= 620*time.Millisecond {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: elapsed=%s, want exactly the five-attempt budget", round, elapsed)
		}
		// Zero writes: no AVAILABLE replacement. Earlier rounds' seeded items are
		// retired by the next round's seed, so they are not replacements -- only
		// an item this entry left live would be.
		var replacement int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE owner_type='rss_source_runtime' AND owner_id=$1 AND id<>$2 AND state='available'`, itemSource, failedID).Scan(&replacement); err != nil {
			t.Fatal(err)
		}
		if replacement != 0 {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: exhaustion must write no replacement, got %d", round, replacement)
		}
		var records int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records WHERE kind='manual_retry' AND owner_id=$1`, itemSource).Scan(&records); err != nil {
			t.Fatal(err)
		}
		if records != 0 {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: exhaustion must record no idempotency, got %d", round, records)
		}
		// Whole-transaction release: the failed item and the lifecycle row must
		// now be acquirable by the blocker.
		if _, err := blocker.Exec(`SELECT id FROM work_item_outcomes WHERE id=$1 FOR UPDATE`, failedID); err != nil {
			blocker.Rollback()
			t.Fatalf("seq=set-exhausts round=%d: the failed work item was not released: %v", round, err)
		}
		var managedAnimeID string
		if err := db.QueryRowContext(ctx, `SELECT managed_anime_id FROM work_item_history WHERE id=$1`, failedID).Scan(&managedAnimeID); err != nil {
			t.Fatal(err)
		}
		if managedAnimeID != "" {
			if _, err := blocker.Exec(`SELECT id FROM managed_anime WHERE id=$1 FOR UPDATE`, managedAnimeID); err != nil {
				blocker.Rollback()
				t.Fatalf("seq=set-exhausts round=%d: the lifecycle row was not released: %v", round, err)
			}
		}
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}
