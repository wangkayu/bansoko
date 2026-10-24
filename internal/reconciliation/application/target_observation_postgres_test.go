package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type reconTargetProbe struct {
	state target.ObservationState
	calls int
}

func (p *reconTargetProbe) Observe(spec target.Spec) target.Observation {
	p.calls++
	size := spec.ExpectedSizeBytes
	return target.Observation{TargetID: spec.ID, State: p.state, ObservedSizeBytes: &size, ObservedAt: time.Now().UTC()}
}

type failingTargetObserver struct{ calls int }

func (o *failingTargetObserver) RefreshObservations(context.Context, string) error {
	o.calls++
	return errors.New("target store unavailable")
}

// seedReconTarget adds the small owner chain one frozen acquisition target
// needs (source -> release -> target) to a seeded reconciliation instance.
func seedReconTarget(t *testing.T, db *sql.DB, animeID, subID, targetPath string) string {
	t.Helper()
	sourceID, releaseID, targetID := "src-"+animeID, "rel-"+animeID, "target-"+animeID
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ($1, $2, 'custom_rss', $3, $3, 'G', 'Show')", []any{sourceID, subID, "https://example.test/" + animeID}},
		{"INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code) VALUES ($1, $2, $3, '{}', '{}', '{}', '{}', 'accepted_pending_download', 'target_work_required')", []any{releaseID, sourceID, releaseID}},
		{"SELECT public.target_upsert($1,$2,'video.mkv','video.mkv','video',$3,4,'ensure_present','block','{}','{}','sha256:test-target')", []any{targetID, releaseID, targetPath}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed target: %v", err)
		}
	}
	return targetID
}

func runReconScan(t *testing.T, service *Service, store *workitems.Store, animeID string) (worker.Outcome, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: animeID, TreeTop: stringPtr("/library")}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rec-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return service.ScanHandler().Handle(ctx, claim)
}

// One scan run refreshes the instance's latest TargetObservation rows (the RSS
// row status's file facts) with the same stat logic the target store uses, and
// the generation completes; there is no separate target scan.
func TestPGCompletedScanRefreshesTargetObservations(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(2)
	seedReconInstance(t, db, "anime-rec-targets", "sub-rec-targets", "active", files)
	targetID := seedReconTarget(t, db, "anime-rec-targets", "sub-rec-targets", files[0].Path)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	probe := &reconTargetProbe{state: target.ObservationPresentMatched}
	service.SetTargetObserver(target.NewScanner(target.NewStore(targetpersistence.NewRepository(db)), probe))

	var before sql.NullString
	if err := db.QueryRow(`SELECT observation_state FROM acquisition_targets WHERE id = $1`, targetID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before.Valid {
		t.Fatalf("target observed before any scan: %s", before.String)
	}
	outcome, err := runReconScan(t, service, store, "anime-rec-targets")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	var state string
	var version int64
	if err := db.QueryRow(`SELECT observation_state, observation_version FROM acquisition_targets WHERE id = $1`, targetID).Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	if state != string(target.ObservationPresentMatched) || version != 1 || probe.calls != 1 {
		t.Fatalf("target observation after scan: state=%s version=%d probes=%d", state, version, probe.calls)
	}
	var scanState string
	if err := db.QueryRow(`SELECT state FROM reconciliation_scans WHERE managed_anime_id = 'anime-rec-targets'`).Scan(&scanState); err != nil {
		t.Fatal(err)
	}
	if scanState != "completed" {
		t.Fatalf("scan state = %s, want completed", scanState)
	}
	for _, table := range []string{"download_tasks", "file_operations"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("the scan created %s rows: %d", table, count)
		}
	}
}

// A failed target pass leaves the generation running (the claim retries from
// its checkpoint) instead of completing with stale RSS row facts.
func TestPGScanTargetPassFailureKeepsGenerationRunning(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(1)
	seedReconInstance(t, db, "anime-rec-target-fail", "sub-rec-target-fail", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	observer := &failingTargetObserver{}
	service.SetTargetObserver(observer)
	if _, err := runReconScan(t, service, store, "anime-rec-target-fail"); err == nil {
		t.Fatal("a failed target pass completed the scan")
	}
	var scanState string
	if err := db.QueryRow(`SELECT state FROM reconciliation_scans WHERE managed_anime_id = 'anime-rec-target-fail'`).Scan(&scanState); err != nil {
		t.Fatal(err)
	}
	if scanState != "running" || observer.calls != 1 {
		t.Fatalf("scan state = %s (target passes %d), want running", scanState, observer.calls)
	}
}

// A first manual scan without a tree top derives the scope from the work's
// placed files instead of failing with SCAN_ROOT_MISSING.
func TestPGFirstManualScanDerivesTreeTopFromPlacements(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(2)
	seedReconInstance(t, db, "anime-rec-first", "sub-rec-first", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	manual := ScanSubmitTriggerManual
	view, err := service.SubmitScan(context.Background(), ScanSubmitInput{ManagedAnimeID: "anime-rec-first", Trigger: &manual})
	if err != nil {
		t.Fatalf("first manual scan: %v", err)
	}
	if view.State != "running" {
		t.Fatalf("first manual scan view = %+v", view)
	}
	var scope string
	if err := db.QueryRow(`SELECT scan_scope->>'tree_top' FROM reconciliation_scans WHERE managed_anime_id = 'anime-rec-first'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "/library/Show/Season 01" {
		t.Fatalf("derived tree top = %q", scope)
	}
	seedReconInstance(t, db, "anime-rec-empty", "sub-rec-empty", "active", nil)
	if _, err := service.SubmitScan(context.Background(), ScanSubmitInput{ManagedAnimeID: "anime-rec-empty", Trigger: &manual}); err == nil {
		t.Fatal("a work with no placed files must still need a tree top")
	}
}
