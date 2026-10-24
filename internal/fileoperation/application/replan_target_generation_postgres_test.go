package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// The acquisition decision belongs to the admitted generation. Replan must
// render its own captured input even when the old target is already satisfied;
// an original-input retry must retain that old target. Expected paths are fixed
// literals, never produced by the planner under test.
func TestPGReplanTargetGeneration(t *testing.T) {
	for _, mode := range []string{"retry", "replan", "replan_old_target_present", "retry_replan"} {
		t.Run(mode, func(t *testing.T) {
			db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
				rules := naming.DefaultNamingRules()
				dir, name := "new", "New.mkv"
				if mode == "retry" {
					dir, name = "old", "Old.mkv"
				}
				rules.TV.DirectoryTemplate, rules.TV.VideoFilenameTemplate = dir, name
				rules.Movie, rules.Unknown = rules.TV, rules.TV
				p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
			})
			ctx := context.Background()
			s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, freshTargetObserver{})
			root := canonicalFixtureRoot(t, f)
			old := filepath.Join(root, "old", "Old.mkv")
			want := old
			if mode != "retry" {
				want = filepath.Join(root, "new", "New.mkv")
			}
			if _, err := ownerTestDB(t).Exec(`INSERT INTO acquisition_targets(id,release_id,target_ref,source_path,media_kind,target_path,expected_size_bytes,intent,collision_policy,render_snapshot,config_snapshot,spec_digest) VALUES ('old-target','release-file-test','primary','source.mkv','video',$1,$2,'ensure_present','block','{}','{}','old-admission')`, old, f.size); err != nil {
				t.Fatal(err)
			}
			if mode == "replan_old_target_present" {
				if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(old, make([]byte, f.size), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := ownerTestDB(t).Exec(`UPDATE acquisition_targets SET observation_state='present_matched',observed_size_bytes=$1,observed_at=CURRENT_TIMESTAMP WHERE id='old-target'`, f.size); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := ownerTestDB(t).BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			c := claimPlanWorkItem(t, db, "generation-worker")
			if c.Owner.ID != planID {
				t.Fatalf("wrong plan claim: %+v", c)
			}
			if mode == "retry" || mode == "retry_replan" {
				if _, err := s.typedPlanError(ctx, c, errors.New("fixture transient planning failure"), false); err != nil {
					t.Fatal(err)
				}
				if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at=statement_timestamp() WHERE id=$1`, c.ID); err != nil {
					t.Fatal(err)
				}
				c = claimPlanWorkItem(t, db, "retry-worker")
			}
			outcome, err := s.PostProcessingPlanHandler().Handle(ctx, c)
			if err != nil || outcome.Kind != runtimeworker.OutcomeCommitted || outcome.OwnerResult.State != "succeeded" {
				t.Fatalf("plan outcome %+v %v", outcome, err)
			}
			if outcome.WriteOwner != nil || outcome.PostTransition != nil {
				t.Fatal("typed Plan used generic Finish")
			}
			rows := loadOperations(t, db, f.artifactID)
			if len(rows) != 1 || rows[0].targetPath != want || rows[0].state != "pending" {
				t.Fatalf("generation %s operations=%+v want=%s", mode, rows, want)
			}
			var effectKey string
			if err := db.QueryRow(`SELECT idempotency_key FROM file_operations WHERE id=$1`, rows[0].id).Scan(&effectKey); err != nil {
				t.Fatal(err)
			}
			if effectKey != stableIdempotencyKey(f.fileID, 1) {
				t.Fatalf("effect identity drift: %s", effectKey)
			}
			// B5 step 7 revival: the typed lane lands the effect at the new
			// frozen target; the old admission target is never written.
			fileClaim, err := (&workitems.Store{DB: db}).ClaimDue(ctx, "effect-worker", []string{workitems.KindFileExecute}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			effect, err := s.Handle(ctx, fileClaim)
			if err != nil || effect.Kind != runtimeworker.OutcomeCommitted || effect.OwnerResult.State != ownerStateCompleted {
				t.Fatalf("effect %+v %v", effect, err)
			}
			got, err := os.ReadFile(want)
			if err != nil || string(got) != "source-content" {
				t.Fatalf("real result=%q %v", got, err)
			}
			if mode == "replan_old_target_present" {
				before, err := os.ReadFile(old)
				if err != nil || string(before) != string(make([]byte, f.size)) {
					t.Fatalf("old target changed=%q %v", before, err)
				}
			} else if mode != "retry" {
				if _, err := os.Stat(old); !os.IsNotExist(err) {
					t.Fatalf("replan wrote old target: %v", err)
				}
			}
		})
	}
}

func TestPGReplanSubtitleIgnoresAdmissionTarget(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		rules := naming.DefaultNamingRules()
		rules.TV.DirectoryTemplate, rules.TV.VideoFilenameTemplate = "new", "New.mkv"
		rules.Movie, rules.Unknown, rules.SubtitleSuffixTemplate = rules.TV, rules.TV, "chs"
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
	})
	ctx := context.Background()
	root := canonicalFixtureRoot(t, f)
	path := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(path, []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "new-sub", "subtitle", "source.chs.ass", path, "subtitle", 8)
	for _, target := range []struct {
		id, ref, source, kind, name string
		size                        int64
	}{
		{"old-video", "primary", "source.mkv", "video", "Old.mkv", f.size},
		{"old-subtitle", "subtitle", "source.chs.ass", "subtitle", "Old.chs.ass", 8},
	} {
		if _, err := ownerTestDB(t).Exec(`INSERT INTO acquisition_targets(id,release_id,target_ref,source_path,media_kind,target_path,expected_size_bytes,intent,collision_policy,render_snapshot,config_snapshot,spec_digest) VALUES ($1,'release-file-test',$2,$3,$4,$5,$6,'ensure_present','block','{}','{}','old-admission')`, target.id, target.ref, target.source, target.kind, filepath.Join(root, "old", target.name), target.size); err != nil {
			t.Fatal(err)
		}
	}
	s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, freshTargetObserver{})
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
	c := claimPlanWorkItem(t, db, "subtitle-generation-worker")
	outcome, err := s.PostProcessingPlanHandler().Handle(ctx, c)
	if err != nil || outcome.Kind != runtimeworker.OutcomeCommitted || outcome.OwnerResult.State != workitems.StateSucceeded || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("plan %+v %v", outcome, err)
	}
	rows := loadOperations(t, db, f.artifactID)
	if len(rows) != 2 {
		t.Fatalf("operations=%+v", rows)
	}
	video, sub := operationForFile(t, rows, f.fileID), operationForFile(t, rows, "new-sub")
	if video.targetPath != filepath.Join(root, "new", "New.mkv") || sub.targetPath != filepath.Join(root, "new", "New.chs.ass") || sub.boundVideoOperationID.String != video.id || sub.state != "pending" {
		t.Fatalf("generation video=%+v subtitle=%+v", video, sub)
	}
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
	// B5 step 7 revival: video then (handed-off) subtitle land at the new
	// frozen generation targets through the typed lane.
	for _, op := range []operationRow{video, sub} {
		c, err := (&workitems.Store{DB: db}).ClaimDue(ctx, "subtitle-effect-worker", []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if c.Owner.ID != op.id {
			t.Fatalf("dependency order: %s want %s", c.Owner.ID, op.id)
		}
		o, err := s.Handle(ctx, c)
		if err != nil || o.Kind != runtimeworker.OutcomeCommitted || o.OwnerResult.State != ownerStateCompleted {
			t.Fatalf("effect %+v %v", o, err)
		}
	}
	if b, err := os.ReadFile(video.targetPath); err != nil || string(b) != "source-content" {
		t.Fatalf("video result=%q %v", b, err)
	}
	if b, err := os.ReadFile(sub.targetPath); err != nil || string(b) != "subtitle" {
		t.Fatalf("subtitle result=%q %v", b, err)
	}
}
