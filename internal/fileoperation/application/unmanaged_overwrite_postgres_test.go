package application

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func unmanagedOverwriteFixturePolicy(p map[string]any) {
	p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "overwrite"}
	p[fixtureUnmanagedOverwriteKey] = true
}

// TestUnmanagedOverwritePlansExchangeWithoutApproval: a present unmanaged
// file under the overwrite setting plans one overwrite operation (frozen
// exchange snapshot) in an ensure_present delivery.
func TestUnmanagedOverwritePlansExchangeWithoutApproval(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, unmanagedOverwriteFixturePolicy)
	defer db.Close()
	target := fixtureIncumbentPath(t, fixture)
	prepareFixture(t, db, newFixtureService(t, db, fixture), fixture.artifactID)
	var collision, intent string
	var snapshot bool
	if err := db.QueryRow(`SELECT collision_policy,intent,overwrite_execution_snapshot IS NOT NULL FROM file_operations WHERE artifact_id=$1 AND target_path=$2`, fixture.artifactID, target).Scan(&collision, &intent, &snapshot); err != nil {
		t.Fatalf("no operation at %s: %v (plan error %q)", target, err, planErrorReason(t, db, fixture.artifactID))
	}
	if collision != "overwrite" || intent != "ensure_present" || !snapshot {
		t.Fatalf("unmanaged overwrite plan collision=%s intent=%s snapshot=%v", collision, intent, snapshot)
	}
}

// TestOverwritePlansManagedTarget: under the overwrite setting a managed
// file at the target (an active or superseded placement no approval names) is
// overwritten like any other file; the old placement is displaced when the new
// one is admitted.
func TestOverwritePlansManagedTarget(t *testing.T) {
	for _, state := range []string{"active", "superseded"} {
		t.Run(state, func(t *testing.T) {
			db, fixture := openFileOperationDBWithPolicy(t, unmanagedOverwriteFixturePolicy)
			defer db.Close()
			target := fixtureIncumbentPath(t, fixture)
			if _, err := ownerTestDB(t).Exec(`INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,facts_version,state) VALUES ('managed-incumbent',$1,$2,'reconciliation','managed-incumbent','fp','fp','frozen',$3)`, target, fixture.animeID, state); err != nil {
				t.Fatal(err)
			}
			prepareFixture(t, db, newFixtureService(t, db, fixture), fixture.artifactID)
			var collision, intent string
			var snapshot bool
			if err := db.QueryRow(`SELECT collision_policy,intent,overwrite_execution_snapshot IS NOT NULL FROM file_operations WHERE artifact_id=$1 AND target_path=$2`, fixture.artifactID, target).Scan(&collision, &intent, &snapshot); err != nil {
				t.Fatalf("no operation at managed target %s: %v (plan error %q)", target, err, planErrorReason(t, db, fixture.artifactID))
			}
			if collision != "overwrite" || intent != "ensure_present" || !snapshot {
				t.Fatalf("managed overwrite plan collision=%s intent=%s snapshot=%v", collision, intent, snapshot)
			}
		})
	}
}

// TestOverwriteTargetManagedPredicate pins the database definition of a
// managed overwrite target and the direct (no approval) delivery branch.
func TestOverwriteTargetManagedPredicate(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	ctx := context.Background()
	owner := ownerTestDB(t)
	for _, row := range []struct{ id, path, state string }{{"p-active", "/m/active.mkv", "active"}, {"p-superseded", "/m/superseded.mkv", "superseded"}} {
		if _, err := owner.Exec(`INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,facts_version,state) VALUES ($1,$2,$3,'reconciliation',$1,'fp','fp','frozen',$4)`, row.id, row.path, fixture.animeID, row.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(`INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,facts_version) VALUES ('p-released','/m/released.mkv',$1,'reconciliation','p-released','fp','fp','frozen')`, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`UPDATE placements SET state='released',released_at=now(),released_by='owner',release_request_key='release-1' WHERE id='p-released'`); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{"/m/active.mkv": true, "/m/superseded.mkv": true, "/m/released.mkv": false, "/m/foreign.mkv": false} {
		var managed bool
		if err := db.QueryRowContext(ctx, `SELECT public.overwrite_target_managed($1)`, path).Scan(&managed); err != nil {
			t.Fatal(err)
		}
		if managed != want {
			t.Fatalf("overwrite_target_managed(%s)=%v want %v", path, managed, want)
		}
	}
	approved := `{"collision":"overwrite","intent":"replace","replacements":[{"expected_object":{"location":{"canonical_path":"/m/approved.mkv"}}}]}`
	for _, c := range []struct {
		delivery, path string
		want           bool
	}{
		{`{"collision":"overwrite","intent":"ensure_present","replacements":[]}`, "/m/foreign.mkv", true},
		{`{"collision":"overwrite","intent":"ensure_present","replacements":[]}`, "/m/active.mkv", true},
		{`{"collision":"block","intent":"ensure_present","replacements":[]}`, "/m/foreign.mkv", false},
		{approved, "/m/approved.mkv", false},
		{approved, "/m/foreign.mkv", true},
	} {
		var got bool
		if err := db.QueryRowContext(ctx, `SELECT public.delivery_direct_overwrite($1::jsonb,$2)`, c.delivery, c.path).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Fatalf("delivery_direct_overwrite(%s,%s)=%v want %v", c.delivery, c.path, got, c.want)
		}
	}
}

// fixtureIncumbentPath finds the incumbent the fixture wrote at the
// planner-rendered target.
func fixtureIncumbentPath(t *testing.T, fixture fileFixture) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(canonicalFixtureRoot(t, fixture), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if got, readErr := os.ReadFile(path); readErr == nil && string(got) == fixtureOverwriteIncumbentBytes {
				found = path
			}
		}
		return nil
	})
	if found == "" {
		t.Fatal("fixture overwrite incumbent not found")
	}
	return found
}

func planErrorReason(t *testing.T, db *sql.DB, artifactID string) string {
	t.Helper()
	var reason string
	if err := db.QueryRow(`SELECT COALESCE(w.last_error_reason,'')||' plan='||p.state||':'||COALESCE(p.outcome_reason,'')||' ops='||COALESCE((SELECT string_agg(o.target_path||'|'||o.state||'|'||o.collision_policy||'|'||COALESCE(o.failure_code,''),';') FROM file_operations o WHERE o.artifact_id=p.artifact_id),'')||' files='||COALESCE((SELECT string_agg(COALESCE(f.plan_kind,'-')||'|'||COALESCE(f.canonical_path,'-')||'|'||COALESCE(f.collision,'-')||'|'||COALESCE(f.delivery_reason_code,'-')||'|'||COALESCE(f.plan_evidence::text,'-'),';') FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id),'') FROM postprocessing_plans p JOIN work_item_history w ON w.kind='postprocess_plan' AND w.owner_id=p.id WHERE p.artifact_id=$1`, artifactID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	return reason
}
