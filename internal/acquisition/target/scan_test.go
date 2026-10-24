package target_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/acquisition/target"
	"bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/disposabledb"
)

type scanProbe struct {
	state target.ObservationState
	calls []target.Spec
}

func (p *scanProbe) Observe(spec target.Spec) target.Observation {
	p.calls = append(p.calls, spec)
	return target.Observation{TargetID: spec.ID, State: p.state, ReasonCode: "test_observation"}
}

func seedScanAnime(t *testing.T, db *sql.DB, animeID, subID, sourceID, releaseID, targetID, state string) {
	t.Helper()
	// This helper is intentionally kept to the small owner chain required by
	// Scanner. It does not create DownloadTask/FileOperation/Placement rows.
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO managed_anime(id, title) VALUES ($1, $2)", []any{animeID, animeID}},
		{"INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ($1, $2, 'custom', $3, $4)", []any{subID, animeID, animeID, state}},
		{"INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ($1, $2, 'custom_rss', $3, $3, 'G', 'Show')", []any{sourceID, subID, "https://example.test/" + releaseID}},
		{"INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code) VALUES ($1, $2, $3, '{}', '{}', '{}', '{}', 'accepted_pending_download', 'target_work_required')", []any{releaseID, sourceID, releaseID}},
		{"SELECT public.target_upsert($1,$2,'video.mkv','video.mkv','video',$3,4,'ensure_present','block','{}','{}','sha256:test-target')", []any{targetID, releaseID, "/library/" + animeID + ".mkv"}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed scan owner: %v", err)
		}
	}
}

func TestScanSubscriptionObservesActiveAndStoppedWithoutCreatingOwners(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedScanAnime(t, db, "anime-active", "sub-active", "src-active", "rel-active", "target-active", "active")
	seedScanAnime(t, db, "anime-stopped", "sub-stopped", "src-stopped", "rel-stopped", "target-stopped", "stopped")

	probe := &scanProbe{state: target.ObservationPresentMatched}
	scanner := target.NewScanner(target.NewStore(persistence.NewRepository(db)), probe)
	ctx := context.Background()
	active, err := scanner.ScanSubscription(ctx, "anime-active")
	if err != nil || len(active) != 1 || active[0].State != target.ObservationPresentMatched || active[0].Version != 1 {
		t.Fatalf("active scan = %+v, err=%v", active, err)
	}
	stopped, err := scanner.ScanSubscription(ctx, "anime-stopped")
	if err != nil || len(stopped) != 1 || stopped[0].State != target.ObservationPresentMatched {
		t.Fatalf("stopped scan = %+v, err=%v", stopped, err)
	}

	probe.state = target.ObservationPresentMismatched
	second, err := scanner.ScanSubscription(ctx, "anime-active")
	if err != nil || len(second) != 1 || second[0].State != target.ObservationPresentMismatched || second[0].Version != 2 {
		t.Fatalf("second scan = %+v, err=%v", second, err)
	}
	if len(probe.calls) != 3 {
		t.Fatalf("scanner must probe exactly frozen targets, calls=%d", len(probe.calls))
	}
	for _, table := range []string{"download_tasks", "file_operations", "placements"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("target scan must not create %s rows: %d", table, count)
		}
	}
}

func TestScanSubscriptionUnknownOrDeletedInstanceIsNotFound(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	scanner := target.NewScanner(target.NewStore(persistence.NewRepository(db)), &scanProbe{state: target.ObservationMissing})
	_, err := scanner.ScanSubscription(context.Background(), "does-not-exist")
	if !errors.Is(err, target.ErrInstanceNotFound) {
		t.Fatalf("unknown instance error = %v", err)
	}
}

// A retired instance keeps its owner graph but admits no observation write. The
// scan must fail closed with the typed retirement boundary (the HTTP boundary
// maps it to 409 MANAGED_ANIME_RETIRED) and must leave the existing observation
// rows untouched.
func TestScanSubscriptionRetiredInstanceFailsClosedWithoutWriting(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedScanAnime(t, db, "anime-retired", "sub-retired", "src-retired", "rel-retired", "target-retired", "stopped")

	probe := &scanProbe{state: target.ObservationPresentMatched}
	scanner := target.NewScanner(target.NewStore(persistence.NewRepository(db)), probe)
	ctx := context.Background()
	if _, err := scanner.ScanSubscription(ctx, "anime-retired"); err != nil {
		t.Fatalf("seed scan before retirement: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1
WHERE id = 'anime-retired'`); err != nil {
		t.Fatal(err)
	}

	probe.calls = nil
	probe.state = target.ObservationPresentMismatched
	_, err := scanner.ScanSubscription(ctx, "anime-retired")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 409 || business.Code != "MANAGED_ANIME_RETIRED" {
		t.Fatalf("retired scan error = %v, want the typed 409 retirement boundary", err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("retired scan must not probe or persist: %d probe calls", len(probe.calls))
	}
	var state string
	var version int64
	if err := db.QueryRowContext(ctx, `
SELECT observation_state, observation_version FROM acquisition_targets WHERE id = 'target-retired'`).Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	if state != string(target.ObservationPresentMatched) || version != 1 {
		t.Fatalf("retired scan mutated observations: state=%s version=%d", state, version)
	}
}
