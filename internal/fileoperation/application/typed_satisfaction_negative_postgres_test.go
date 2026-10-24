package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// preparedObservedBatch plans the observed fixture — one file that already sits
// at the absolute path the frozen naming templates render, `observed/Main.mkv`
// under the fixture root. It returns that rendered target so a test can arrange
// or inspect the object the plan will be satisfied by.
func preparedObservedBatch(t *testing.T) (*sql.DB, *Service, workitems.Claim, *artifactPreparedBatch, string) {
	t.Helper()
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		rules := naming.DefaultNamingRules()
		rules.TV.DirectoryTemplate, rules.TV.VideoFilenameTemplate = "observed", "Main.mkv"
		rules.Movie, rules.Unknown = rules.TV, rules.TV
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
	})
	// The template body is relative, so the frozen policy is re-rooted under the
	// fixture directory: the render, not a library anchor, makes it absolute.
	path := filepath.Join(canonicalFixtureRoot(t, f), "observed", "Main.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source-content"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newFixtureServiceWithPorts(t, db, f, &testExecutor{}, filesystem.OSFileObserver{})
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	c := claimPlanWorkItem(t, db, "observed-negative")
	p, err := s.loadPlan(ctx, id)
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
	return db, s, c, b, path
}

func TestTypedObservedSatisfactionRejectsResignedMalformedProof(t *testing.T) {
	for _, mode := range []string{"extra_object", "extra_location", "extra_identity", "bad_kind", "bad_file_type", "bad_inode", "wrong_path", "wrong_namespace", "wrong_size", "wrong_expected", "time_format", "wrong_root", "wrong_spec"} {
		t.Run(mode, func(t *testing.T) {
			db, s, c, b, _ := preparedObservedBatch(t)
			d, err := buildTypedPlanDeclarations(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			var plan map[string]any
			decoder := json.NewDecoder(strings.NewReader(d.Plan))
			decoder.UseNumber()
			if err := decoder.Decode(&plan); err != nil {
				t.Fatal(err)
			}
			item := plan["items"].([]any)[0].(map[string]any)
			e := item["evidence"].(map[string]any)
			o := e["observation"].(map[string]any)
			obj := o["observed_object"].(map[string]any)
			location := obj["location"].(map[string]any)
			identity := obj["identity"].(map[string]any)
			switch mode {
			case "extra_object":
				obj["trusted"] = true
			case "extra_location":
				location["trusted"] = true
			case "extra_identity":
				identity["trusted"] = true
			case "bad_kind":
				identity["kind"] = "remote"
			case "bad_file_type":
				obj["file_type"] = "directory"
			case "bad_inode":
				identity["inode"] = "00"
			case "wrong_path":
				location["canonical_path"] = "/different"
			case "wrong_namespace":
				location["namespace_digest"] = "invalid"
			case "wrong_size":
				obj["size"] = int64(99)
			case "wrong_expected":
				o["expected_size"] = int64(99)
			case "time_format":
				// PathObjectV1 carries one observation time (no read window to
				// order); it must be canonical UTC.
				obj["observed_at"] = "2026-10-09T01:02:03+00:00"
			case "wrong_root":
				o["acquisition_id"] = "other"
			case "wrong_spec":
				o["spec_digest"] = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			}
			canonical, err := canonicalPlanValue(o)
			if err != nil {
				t.Fatal(err)
			}
			e["canonical"] = canonical
			e["digest"] = protocol.Digest("acquisition-observed-satisfaction:v1", []byte(canonical))
			e["final_object"] = obj
			d.Plan, err = canonicalPlanValue(plan)
			if err != nil {
				t.Fatal(err)
			}
			var capture map[string]json.RawMessage
			json.Unmarshal(b.capture.Facts, &capture)
			capture["prepared_bindings"] = json.RawMessage(`[]`)
			raw, _ := json.Marshal(capture)
			before := planGraphSnapshot(t, db)
			err = s.workItems.WithTypedStageTransaction(context.Background(), c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
				var reply []byte
				err := scope.SQLTx().QueryRowContext(context.Background(), `SELECT postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, b.capture.PlanInputDigest, b.input, d.Requirements, d.Targets, d.Plan, typedExecuteOperations(b, d), raw).Scan(&reply)
				return workitems.DispositionNoExternalEffect, err
			})
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "PLAN_OBSERVED_SATISFACTION_INVALID" {
				t.Fatalf("proof=%s err=%v", mode, err)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("invalid proof partially committed")
			}
		})
	}
}
