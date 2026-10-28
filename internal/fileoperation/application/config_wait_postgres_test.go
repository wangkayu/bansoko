package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	animeapplication "bansoko/internal/anime/application"
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// currentRulesSource serves the anime's current naming rules, which a test
// edits the way a rules PATCH does; the frozen rules the plan executes stay
// the fixture's.
type currentRulesSource struct {
	rules *naming.NamingRules
	root  string
}

func (s currentRulesSource) EffectiveNamingRules(context.Context, string) (naming.NamingRules, error) {
	return fixtureAbsoluteNamingRules(*s.rules, s.root), nil
}

// openConfigWaitFixture is the parity fixture (frozen default rules, both
// bindings bound) with the production configuration gate wired and one
// queued subscription plan.
func openConfigWaitFixture(t *testing.T, current *naming.NamingRules) (*sql.DB, fileFixture, *Service, string) {
	t.Helper()
	db, fixture := openParityFixtureDB(t, naming.DefaultNamingRules())
	t.Cleanup(func() { db.Close() })
	seedParityFrozenEvidence(t, db, fixture)
	service := newFixtureService(t, db, fixture)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	service.SetCurrentConfigGate(filemaintenance.NewNamingTargetRenderer(db, currentRulesSource{rules: current, root: fixture.root}))
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, fixture, service, planID
}

// runPlanClaim claims the plan's due work item and runs the plan stage once.
func runPlanClaim(t *testing.T, db *sql.DB, service *Service, planID string) {
	t.Helper()
	claim := claimPlanWorkItem(t, db, "config-wait-planner")
	if claim.Owner.ID != planID {
		t.Fatalf("claimed %s instead of plan %s", claim.Owner.ID, planID)
	}
	out, err := service.PostProcessingPlanHandler().Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != runtimeworker.OutcomeCommitted {
		t.Fatalf("plan claim outcome = %+v", out)
	}
}

// assertWaitingForConfiguration requires the plan to wait with the hold: no
// attempt spent, no file operation, the root running and the work item
// parked with its attempt refunded, so nothing claims it now.
func assertWaitingForConfiguration(t *testing.T, db *sql.DB, fixture fileFixture, planID, hold string) {
	t.Helper()
	ctx := context.Background()
	owner := ownerTestDB(t)
	var state, root string
	var matches bool
	var files, attempts int
	if err := owner.QueryRowContext(ctx, `
SELECT p.state, p.config_hold = $2::jsonb, p.config_hold_files, p.plan_attempts_started, r.state
FROM postprocessing_plans p JOIN acquisitions r ON r.id = p.acquisition_id WHERE p.id = $1`, planID, hold).
		Scan(&state, &matches, &files, &attempts, &root); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_config" || !matches || files != 1 || attempts != 0 || root != "running" {
		t.Fatalf("plan %s hold-matches=%v files=%d attempts=%d root=%s", state, matches, files, attempts, root)
	}
	var workState string
	var attempt int
	var later bool
	if err := owner.QueryRowContext(ctx, `SELECT state, attempt, available_at > clock_timestamp() + interval '50 minutes' FROM work_items WHERE owner_id = $1 AND kind = 'postprocess_plan'`, planID).
		Scan(&workState, &attempt, &later); err != nil {
		t.Fatal(err)
	}
	if workState != "available" || attempt != 0 || !later {
		t.Fatalf("work item %s attempt=%d parked=%v", workState, attempt, later)
	}
	var operations int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("a waiting plan created %d file operations", operations)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	if _, err := store.ClaimDue(ctx, "config-wait-early", []string{workitems.KindPostProcessPlan}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("a parked plan was claimable: %v", err)
	}
}

// assertPlanned requires the plan to have planned its file once the
// configuration is complete: succeeded, the hold cleared, one operation.
func assertPlanned(t *testing.T, fixture fileFixture, planID string) {
	t.Helper()
	ctx := context.Background()
	owner := ownerTestDB(t)
	var state string
	var cleared bool
	if err := owner.QueryRowContext(ctx, `SELECT state, config_hold IS NULL AND config_hold_files IS NULL AND config_hold_since IS NULL FROM postprocessing_plans WHERE id = $1`, planID).Scan(&state, &cleared); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || !cleared {
		t.Fatalf("plan %s, hold cleared %v", state, cleared)
	}
	var operations int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("file operations = %d, want 1", operations)
	}
}

const (
	clearTMDB   = `UPDATE anime_metadata SET tmdb_work_id = NULL, tmdb_media_kind = NULL, tmdb_season = NULL WHERE managed_anime_id = $1`
	restoreTMDB = `UPDATE anime_metadata SET tmdb_work_id = 'tmdb-original', tmdb_media_kind = 'tv', tmdb_season = 1 WHERE managed_anime_id = $1`
)

// Without a TMDB binding the subscription plan waits instead of planning; the
// configuration write that binds it again wakes the plan in its own
// transaction, and the next claim plans the file.
func TestPlanWaitsForTheTMDBBindingAndResumesOnTheWrite(t *testing.T) {
	current := naming.DefaultNamingRules()
	db, fixture, service, planID := openConfigWaitFixture(t, &current)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, clearTMDB, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	runPlanClaim(t, db, service, planID)
	assertWaitingForConfiguration(t, db, fixture, planID, `[{"item":"binding_missing:tmdb","files":1}]`)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, restoreTMDB, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	if err := animeapplication.WakeConfigWaitingPlansTx(ctx, tx, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	runPlanClaim(t, db, service, planID)
	assertPlanned(t, fixture, planID)
}

// A template variable the current render does not define makes the plan wait
// naming it. A wake without a fix waits again with the same hold (its
// revision unchanged, so nothing is logged twice); fixing the template and
// waking plans the file.
func TestPlanWaitsForAnUndefinedTemplateVariable(t *testing.T) {
	current := naming.DefaultNamingRules()
	current.TV.VideoFilenameTemplate = "${anibt.language}.${file.ext}"
	db, fixture, service, planID := openConfigWaitFixture(t, &current)
	ctx := context.Background()
	hold := `[{"item":"template_variable_undefined:anibt.language","files":1}]`
	runPlanClaim(t, db, service, planID)
	assertWaitingForConfiguration(t, db, fixture, planID, hold)
	revision := func() int {
		t.Helper()
		var value int
		if err := ownerTestDB(t).QueryRowContext(ctx, `SELECT config_hold_revision FROM postprocessing_plans WHERE id = $1`, planID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	wake := func() {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := animeapplication.WakeConfigWaitingPlansTx(ctx, tx, fixture.animeID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	before := revision()

	wake()
	runPlanClaim(t, db, service, planID)
	assertWaitingForConfiguration(t, db, fixture, planID, hold)
	if after := revision(); after != before {
		t.Fatalf("an unchanged hold moved the revision %d -> %d", before, after)
	}

	current = naming.DefaultNamingRules()
	wake()
	runPlanClaim(t, db, service, planID)
	assertPlanned(t, fixture, planID)
}

// A configuration completed without a wake (the write raced the claim) is
// reached by the periodic sweep.
func TestSweepWakesAPlanWhoseConfigurationWasCompletedWithoutAWake(t *testing.T) {
	current := naming.DefaultNamingRules()
	db, fixture, service, planID := openConfigWaitFixture(t, &current)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, clearTMDB, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	runPlanClaim(t, db, service, planID)
	if _, err := db.ExecContext(ctx, restoreTMDB, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	assertWaitingForConfiguration(t, db, fixture, planID, `[{"item":"binding_missing:tmdb","files":1}]`)

	woken, err := animeapplication.SweepConfigWaitingPlans(ctx, db)
	if err != nil || woken != 1 {
		t.Fatalf("sweep woke %d: %v", woken, err)
	}
	runPlanClaim(t, db, service, planID)
	assertPlanned(t, fixture, planID)
}
