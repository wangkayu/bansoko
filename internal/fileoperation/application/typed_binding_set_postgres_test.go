package application

import (
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"testing"
)

func TestTypedBindingSetRejectsIncompleteAndForeignPreparedDecisions(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "foreign", "wrong_video", "null_state", "extra_decision_key", "invalid_confidence"} {
		t.Run(mode, func(t *testing.T) {
			db, s, c, b := preparedSubtitleBatchFixture(t)
			ctx := context.Background()
			d, err := buildTypedPlanDeclarations(ctx, b)
			if err != nil {
				t.Fatal(err)
			}
			var capture map[string]json.RawMessage
			json.Unmarshal(b.capture.Facts, &capture)
			var payload struct {
				Bindings []map[string]any `json:"bindings"`
			}
			json.Unmarshal(batchPayload(b), &payload)
			switch mode {
			case "missing":
				payload.Bindings = nil
			case "duplicate":
				payload.Bindings = append(payload.Bindings, payload.Bindings[0])
			case "foreign":
				payload.Bindings[0]["file"] = "foreign-file"
			case "wrong_video":
				payload.Bindings[0]["bound_ref"] = "foreign-video"
			case "null_state":
				payload.Bindings[0]["state"] = nil
			case "extra_decision_key":
				payload.Bindings[0]["decision"].(map[string]any)["caller_flag"] = true
			case "invalid_confidence":
				payload.Bindings[0]["decision"].(map[string]any)["confidence"] = 1.1
			}
			capture["prepared_bindings"], _ = json.Marshal(payload.Bindings)
			if mode == "missing" {
				capture["prepared_bindings"] = json.RawMessage(`[]`)
			}
			raw, _ := json.Marshal(capture)
			before := planGraphSnapshot(t, db)
			err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
				var reply []byte
				err := scope.SQLTx().QueryRowContext(ctx, `SELECT postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, b.capture.PlanInputDigest, b.input, d.Requirements, d.Targets, d.Plan, typedExecuteOperations(b, d), raw).Scan(&reply)
				return workitems.DispositionNoExternalEffect, err
			})
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "PLAN_BINDING_SET" {
				t.Fatalf("prepared binding rejection=%v", err)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("rejected decision changed graph")
			}
		})
	}
}

func preparedSubtitleBatchFixture(t *testing.T) (*sql.DB, *Service, workitems.Claim, *artifactPreparedBatch) {
	t.Helper()
	db, f := openFileOperationDBWithPolicy(t, bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"}))
	if err := os.WriteFile(f.sourcePath+".ass", []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "set-sub", "subtitle", "source.chs.ass", f.sourcePath+".ass", "subtitle", 8)
	s := newFixtureService(t, db, f)
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	c := claimPlanWorkItem(t, db, "binding-set")
	p, err := s.loadPlan(ctx, c.Owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.captureArtifactBatch(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.buildArtifactBatch(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	return db, s, c, b
}
