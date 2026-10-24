package application

import (
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"testing"
	"time"
)

type captureDriftingObserver struct {
	FileObserver
	paths    domain.PathObserver
	owner    *sql.DB
	artifact string
	changed  bool
}

func (o *captureDriftingObserver) ObservePath(ctx context.Context, path string) (domain.PathObservation, error) {
	out, err := o.paths.ObservePath(ctx, path)
	if err == nil && !o.changed {
		o.changed = true
		_, err = o.owner.ExecContext(ctx, `UPDATE artifact_files SET facts='{"changed":true}' WHERE artifact_id=$1`, o.artifact)
	}
	return out, err
}
func TestTypedObservedCaptureDriftRetriesFreshPreparation(t *testing.T) {
	db, s, c, b, _ := preparedObservedBatch(t)
	original := s.observer
	s.observer = &captureDriftingObserver{FileObserver: original, paths: original.(domain.PathObserver), owner: ownerTestDB(t), artifact: b.capture.ArtifactID}
	ctx := context.Background()
	if _, err := s.handlePostProcessPlan(ctx, c); err != nil {
		t.Fatal(err)
	}
	var state, code string
	var attempt, items int
	if err := db.QueryRow(`SELECT state,last_error_code,attempt FROM work_item_history WHERE id=$1`, c.ID).Scan(&state, &code, &attempt); err != nil {
		t.Fatal(err)
	}
	if state != "available" || code != "PLAN_CAPTURE_CHANGED" || attempt != c.Attempt {
		t.Fatalf("stale handling=%s/%s/%d", state, code, attempt)
	}
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.id=$1 AND f.plan_kind IS NOT NULL`, c.Owner.ID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 0 {
		t.Fatal("stale candidate sealed")
	}
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET available_at=statement_timestamp()-interval '1 second' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db}
	next, err := store.ClaimDue(ctx, "capture-retry", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != c.ID || next.Attempt != c.Attempt+1 {
		t.Fatal("retry changed work or budget")
	}
	s.observer = original
	if _, err := s.handlePostProcessPlan(ctx, next); err != nil {
		t.Fatal(err)
	}
	assertCommittedReentry(t, s, "artifact-file-test", nil, "confirmed", "")
}
