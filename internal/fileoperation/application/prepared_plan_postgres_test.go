package application

import (
	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func preparedPlanPGFixture(t *testing.T) (*Service, workitems.Claim) {
	t.Helper()
	db, f := openFileOperationDB(t)
	s := newFixtureService(t, db, f)
	tx, err := ownerTestDB(t).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seedCommittedInspectionPlanTx(context.Background(), txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, claimPlanWorkItem(t, db, "prepared-plan")
}

type blockingPlanParentAuthorizer struct {
	entered chan struct{}
	release chan struct{}
}

func (a blockingPlanParentAuthorizer) ParentIdentities(string, string, string) (domain.FileIdentity, domain.FileIdentity, error) {
	close(a.entered)
	<-a.release
	return domain.FileIdentity{Device: 1, Inode: 2}, domain.FileIdentity{Device: 1, Inode: 3}, nil
}

func overwriteBarrierFixture(t *testing.T) (*Service, workitems.Claim) {
	t.Helper()
	directory := t.TempDir()
	target := filepath.Join(directory, "target.mkv")
	if err := os.WriteFile(target, []byte("existing target"), 0600); err != nil {
		t.Fatal(err)
	}
	db, f := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		rules := naming.DefaultNamingRules()
		encodedDirectory := directory
		rules.TV.DirectoryTemplate = encodedDirectory
		rules.Movie.DirectoryTemplate = encodedDirectory
		rules.Unknown.DirectoryTemplate = encodedDirectory
		rules.TV.VideoFilenameTemplate = "target.mkv"
		rules.Movie.VideoFilenameTemplate = "target.mkv"
		rules.Unknown.VideoFilenameTemplate = "target.mkv"
		policy["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
		policy["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "overwrite"}
		policy["route_root"] = directory
	})
	s := newFixtureService(t, db, f)
	tx, err := ownerTestDB(t).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedCommittedInspectionPlanTx(context.Background(), txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, claimPlanWorkItem(t, db, "prepared-overwrite")
}

// Exercise the actual handler's filesystem I/O, not an artificial callback.
func TestPGPreparedPlanHandlerBuilderBarrierRejectsDriftWithoutOwnerLocks(t *testing.T) {
	for _, mode := range []string{"cancel", "artifact", "replan"} {
		t.Run(mode, func(t *testing.T) {
			s, c := overwriteBarrierFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			entered := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			s.parentAuthorizer = blockingPlanParentAuthorizer{entered: entered, release: release}
			go func() { _, err := s.handlePostProcessPlan(ctx, c); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("handler never reached transaction-free builder")
			}
			fixtureDB := workItemFixtureDB(t)
			tx, err := fixtureDB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var artifactID string
			if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM postprocessing_plans WHERE id=$1 FOR UPDATE NOWAIT`, c.Owner.ID).Scan(&artifactID); err != nil {
				_ = tx.Rollback()
				t.Fatal("builder held plan lock", err)
			}
			if _, err := tx.ExecContext(ctx, `SELECT id FROM artifacts WHERE id=$1 FOR UPDATE NOWAIT`, artifactID); err != nil {
				_ = tx.Rollback()
				t.Fatal("builder held Artifact lock", err)
			}
			switch mode {
			case "artifact":
				_, err = tx.ExecContext(ctx, `UPDATE artifacts SET version=version+1 WHERE id=$1`, artifactID)
			case "replan":
				_, err = tx.ExecContext(ctx, `UPDATE postprocessing_plans SET version=version+1 WHERE id=$1`, c.Owner.ID)
			case "cancel":
				_, err = tx.ExecContext(ctx, `UPDATE work_items SET cancel_requested=true WHERE id=$1`, c.ID)
			}
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			close(release)
			err = <-done
			var pg *pgconn.PgError
			if !errors.Is(err, workitems.ErrStaleClaim) && !(errors.As(err, &pg) && pg.Code == "23514" && pg.Message == "PLAN_STAGE_STALE_CLAIM") {
				t.Fatalf("handler must reject stale capture: %v", err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, artifactID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("stale handler persisted operations")
			}
		})
	}
}
func TestPGPreparedPlanSlowBuilderHoldsNoOwnerLockAndRejectsCancel(t *testing.T) {
	s, c := overwriteBarrierFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	s.parentAuthorizer = blockingPlanParentAuthorizer{entered: entered, release: release}
	go func() { _, err := s.PostProcessingPlanHandler().Handle(ctx, c); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("handler did not reach real builder I/O")
	}
	tx, err := workItemFixtureDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM artifacts WHERE id=(SELECT artifact_id FROM postprocessing_plans WHERE id=$1) FOR UPDATE NOWAIT`, c.Owner.ID); err != nil {
		tx.Rollback()
		t.Fatal("slow builder held Artifact lock", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM postprocessing_plans WHERE id=$1 FOR UPDATE NOWAIT`, c.Owner.ID); err != nil {
		tx.Rollback()
		t.Fatal("slow builder held Plan lock", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: s.db}
	if err := store.RequestCancel(ctx, c, "cancel during slow prepare"); err != nil {
		t.Fatal(err)
	}
	close(release)
	err = <-done
	var pg *pgconn.PgError
	if !errors.Is(err, workitems.ErrStaleClaim) && !(errors.As(err, &pg) && pg.Code == "23514" && pg.Message == "PLAN_STAGE_STALE_CLAIM") {
		t.Fatalf("cancel crossed typed commit: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=$1`, c.Owner.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("cancelled builder committed Operation")
	}
}

// Snapshot every application table, including indirect trigger and settle writes.
func planGraphSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	// Full evidence audit includes owner-only credential/binding tables. The
	// caller's handler remains on runtime; snapshots use the auditor role.
	db = ownerTestDB(t)
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	graph := map[string]json.RawMessage{}
	for _, table := range tables {
		var raw []byte
		if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(r ORDER BY r::text),'[]'::jsonb) FROM (SELECT to_jsonb(x) AS r FROM public.` + pgx.Identifier{table}.Sanitize() + ` x) q`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		graph[table] = raw
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPGPreparedPlanCaptureDriftRejectsShortCommit(t *testing.T) {
	for _, change := range []string{"artifact", "plan", "binding"} {
		t.Run(change, func(t *testing.T) {
			s, c := preparedPlanPGFixture(t)
			ctx := context.Background()
			plan, err := s.loadPlan(ctx, c.Owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := s.captureArtifactBatch(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.buildArtifactBatch(ctx, batch, nil); err != nil {
				t.Fatal(err)
			}
			query := map[string]string{"artifact": `UPDATE artifacts SET version=version+1 WHERE id=$1`, "plan": `UPDATE postprocessing_plans SET version=version+1 WHERE id=$1`, "binding": `UPDATE artifact_files SET facts='{"changed":true}' WHERE artifact_id=$1`}[change]
			id := c.Owner.ID
			if change == "artifact" || change == "binding" {
				id = batch.artifact.artifactID
			}
			before := planGraphSnapshot(t, s.db)
			_, err = ownerTestDB(t).Exec(query, id)
			if err != nil {
				t.Fatal(err)
			}
			before = planGraphSnapshot(t, s.db)
			_, err = s.commitTypedPlan(ctx, c, batch)
			var pg *pgconn.PgError
			expected := "PLAN_STAGE_STALE_CLAIM"
			if change == "binding" {
				expected = "PLAN_CAPTURE_CHANGED"
			}
			if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != expected {
				t.Fatalf("%s drift crossed typed commit: %v", change, err)
			}
			if after := planGraphSnapshot(t, s.db); after != before {
				t.Fatalf("%s rejected commit changed graph", change)
			}
		})
	}
}

func TestPGPreparedPlanProductionPersistFailureRollsBackOperationAndWorkItem(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	plan, err := s.loadPlan(ctx, c.Owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := s.captureArtifactBatch(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.buildArtifactBatch(ctx, batch, nil); err != nil {
		t.Fatal(err)
	}
	owner := workItemFixtureDB(t)
	_, err = owner.Exec(`CREATE FUNCTION public.test_plan_persist_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.owner_type='file_operation' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='TEST_PLAN_PERSIST_FAULT'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_plan_persist_fault AFTER INSERT ON public.work_items FOR EACH ROW EXECUTE FUNCTION public.test_plan_persist_fault()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(`DROP TRIGGER test_plan_persist_fault ON public.work_items; DROP FUNCTION public.test_plan_persist_fault()`); err != nil {
			t.Error(err)
		}
	})
	before := planGraphSnapshot(t, s.db)
	_, err = s.commitTypedPlan(ctx, c, batch)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "TEST_PLAN_PERSIST_FAULT" {
		t.Fatalf("fault not reached: %v", err)
	}
	if after := planGraphSnapshot(t, s.db); after != before {
		t.Fatal("failed typed commit changed graph")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=$1`, c.Owner.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("operation leaked from failed commit")
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type='file_operation' AND acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`, c.Owner.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("downstream WorkItem leaked")
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM postprocessing_plans WHERE id=$1`, c.Owner.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatal("plan advanced despite rollback")
	}
}
