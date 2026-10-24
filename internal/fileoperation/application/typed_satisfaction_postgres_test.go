package application

import (
	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTypedPlanObservedSatisfactionBranches(t *testing.T) {
	for _, scenario := range []string{"adopt", "retain", "block_mismatch", "noop_mismatch", "version_mismatch", "unavailable", "after_read_deleted", "after_read_modified", "after_read_replaced"} {
		t.Run(scenario, func(t *testing.T) {
			db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {

				rules := naming.DefaultNamingRules()
				rules.TV.DirectoryTemplate, rules.TV.VideoFilenameTemplate = "observed", "Main.mkv"
				rules.Movie, rules.Unknown = rules.TV, rules.TV
				p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
				effect := map[string]any{"operation": "copy", "collision_policy": "fail"}
				if scenario == "retain" || scenario == "noop_mismatch" {
					effect["operation"] = "noop"
				}
				if scenario == "version_mismatch" {
					effect["collision_policy"] = "versioned"
				}
				p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = effect
			})
			target := filepath.Join(canonicalFixtureRoot(t, f), "observed", "Main.mkv")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			bytes := []byte("source-content")
			if scenario == "block_mismatch" || scenario == "noop_mismatch" || scenario == "version_mismatch" {
				// Path + size is identity: only a different size is a mismatch.
				bytes = make([]byte, len(bytes)+3)
			}
			if err := os.WriteFile(target, bytes, 0600); err != nil {
				t.Fatal(err)
			}
			observer := FileObserver(filesystem.OSFileObserver{})
			if scenario == "unavailable" {
				observer = freshTargetObserver{}
			}
			s := newFixtureServiceWithPorts(t, db, f, &testExecutor{}, observer)
			// Build the real plan/work via the existing authorized arrangement.
			tx, err := ownerTestDB(t).BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			planID, err := seedCommittedInspectionPlanTx(context.Background(), txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			claim := claimPlanWorkItem(t, db, "observed-branch")
			plan, err := s.loadPlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := s.captureArtifactBatch(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.buildArtifactBatch(context.Background(), batch, nil); err != nil {
				t.Fatal(err)
			}
			if scenario == "after_read_modified" {
				if err := os.WriteFile(target, []byte("modified bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "after_read_replaced" {
				if err := os.Rename(target, target+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("modified bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "after_read_deleted" {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.commitTypedPlan(context.Background(), claim, batch); err != nil {
				t.Fatal(err)
			}
			var kind string
			if err := db.QueryRow(`SELECT f.plan_kind FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.id=$1`, planID).Scan(&kind); err != nil {
				t.Fatal(err)
			}
			want := "adopt"
			switch scenario {
			case "retain":
				want = "retain"
			case "block_mismatch", "noop_mismatch", "unavailable":
				want = "failed"
			case "version_mismatch":
				want = "execute"
			}
			if kind != want {
				t.Fatalf("kind=%s want=%s", kind, want)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=$1`, planID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if (count == 1) != (kind == "execute") {
				t.Fatalf("pseudo/missing Operation: %d", count)
			}
			if kind == "adopt" || kind == "retain" {
				verifyObservedSatisfactionSQLConsumption(t, ownerTestDB(t), planID)
				var evidence []byte
				if err := db.QueryRow(`SELECT receipt FROM acquisition_files WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND outcome IS NOT NULL`, planID).Scan(&evidence); err != nil {
					t.Fatal(err)
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(evidence, &wire); err != nil {
					t.Fatal(err)
				}
				if len(wire) != 4 {
					t.Fatal("incomplete proof")
				}
			}

			assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
		})
	}
}

var _ domain.PathObserver = filesystem.OSFileObserver{}
