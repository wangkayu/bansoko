package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/disposabledb/guard"
)

// Owner-arranged relational fixture verifies capture recheck; the independent
// filemaintenance test supplies the actual decision producer positive.
func TestReplacementCaptureTransactionRecheck(t *testing.T) {
	db := disposabledb.ResetDisposableSchemaAsOwner(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "incumbent")
	data := []byte("incumbent")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Path-level verification: an eligible placement records the fingerprint
	// document of its FileOperation, whose size_bytes is the approved
	// incumbent size (a bare digest records none and fails closed).
	digest := fmt.Sprintf(`{"algorithm":"sha256","value":"%x","size_bytes":%d}`, sha256.Sum256(data), len(data))
	for _, stmt := range []string{`INSERT INTO managed_anime(id,title) VALUES('anime-1','Show')`, `INSERT INTO subscriptions(id,managed_anime_id,origin,instance_label,state) VALUES('sub-1','anime-1','custom','Show','active')`, `INSERT INTO anime_business_configs(managed_anime_id,admission_mode,quality_profile,replacement_source_default_version,replacement_version) VALUES('anime-1','single_copy','{"version":"profile-v2","items":[{"key":"rt.resolution","ranks":[]}]}',1,1)`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,facts_version) VALUES('placement',$1,'anime-1','reconciliation','scan','source',$2,'facts-v1')`, path, digest); err != nil {
		t.Fatal(err)
	}
	profile := json.RawMessage(`{"version":"profile-v2","items":[{"key":"rt.resolution","ranks":[["1080p"],["720p"]]}]}`)
	sourceDigest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pools, _ := json.Marshal([]any{map[string]any{"pool_key": "tv:e1", "verdict": "replace", "winner": map[string]any{"placement_id": "release:release", "version": 1, "fingerprint": "candidate"}, "losers": []any{map[string]any{"placement_id": "placement", "version": 1, "fingerprint": digest}}, "comparisons": []any{map[string]any{"placement": map[string]any{"placement_id": "placement", "version": 1, "fingerprint": digest}, "profile": profile, "candidate": map[string]any{"resolution": "1080p", "source_digest": sourceDigest, "values": map[string][]string{"rt.resolution": {"1080p"}}}, "existing": map[string]any{"resolution": "720p", "source_digest": sourceDigest, "values": map[string][]string{"rt.resolution": {"720p"}}}, "dimensions": []string{"resolution"}, "verdict": "replace"}}}})
	if _, err := db.Exec(`INSERT INTO replacement_decisions(id,idempotency_key,managed_anime_id,candidate_release_id,policy_version,profile_version,candidate_facts_ref,coverage_key,coverage_keys,admission_mode,pools,verdict,state,subscription_version,input_digest) VALUES('decision','key','anime-1','release',1,'profile-v2','release:1','tv:e1','["tv:e1"]','single_copy',$1,'replace','admitted',1,'digest')`, string(pools)); err != nil {
		t.Fatal(err)
	}
	runtimeDB := guard.MustRuntime(t)
	defer runtimeDB.Close()
	capture, err := CaptureReplacement(ctx, runtimeDB, "release", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := runtimeDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capture.recheck(ctx, tx, "release", "anime-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	material := qualificationFixture(t, "automatic_source")
	material.release.id = "release"
	material.config.business.Sections["file_effect"] = json.RawMessage(`{"operation":"copy","collision_policy":"overwrite"}`)
	c := admissionCapture{material: material}
	projected, err := productionIntent(ctx, tx, c, "automatic_source", "", capture)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if len(projected.delivery.Object["replacements"].Array) != 1 || projected.delivery.Object["intent"].String != "replace" {
		tx.Rollback()
		t.Fatal("approved replacement not frozen")
	}
	// Without a quality approval, the frozen global policy still governs
	// every target, including one not present until after acceptance.
	absent, err := productionIntent(ctx, tx, c, "automatic_source", "")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if absent.delivery.Object["intent"].String != "ensure_present" || absent.delivery.Object["collision"].String != "overwrite" || len(absent.delivery.Object["replacements"].Array) != 0 {
		tx.Rollback()
		t.Fatalf("absent overwrite target delivery = intent %q collision %q replacements %d", absent.delivery.Object["intent"].String, absent.delivery.Object["collision"].String, len(absent.delivery.Object["replacements"].Array))
	}
	// A batch with one present approved target and one absent target keeps
	// the overwrite delivery with the one approval; unapproved targets still
	// follow the frozen global collision policy.
	mixed, err := productionIntent(ctx, tx, c, "automatic_source", "", capture)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if mixed.delivery.Object["collision"].String != "overwrite" || len(mixed.delivery.Object["replacements"].Array) != 1 {
		tx.Rollback()
		t.Fatalf("mixed batch delivery = collision %q replacements %d", mixed.delivery.Object["collision"].String, len(mixed.delivery.Object["replacements"].Array))
	}
	// An approved replacement takes effect above the global policy: under
	// skip (fail → block) and version the delivery keeps that collision for
	// every other target and freezes intent replace with the approval, so the
	// file plan overwrites the approved path alone.
	for policy, want := range map[string]string{"fail": "block", "versioned": "version"} {
		material.config.business.Sections["file_effect"] = json.RawMessage(`{"operation":"copy","collision_policy":"` + policy + `"}`)
		c := admissionCapture{material: material}
		approved, err := productionIntent(ctx, tx, c, "automatic_source", "", capture)
		if err != nil {
			tx.Rollback()
			t.Fatalf("%s: %v", policy, err)
		}
		if approved.delivery.Object["intent"].String != "replace" || approved.delivery.Object["collision"].String != want || len(approved.delivery.Object["replacements"].Array) != 1 {
			tx.Rollback()
			t.Fatalf("%s approved delivery = intent %q collision %q replacements %d", policy, approved.delivery.Object["intent"].String, approved.delivery.Object["collision"].String, len(approved.delivery.Object["replacements"].Array))
		}
		plain, err := productionIntent(ctx, tx, c, "automatic_source", "")
		if err != nil {
			tx.Rollback()
			t.Fatalf("%s: %v", policy, err)
		}
		if plain.delivery.Object["intent"].String != "ensure_present" || plain.delivery.Object["collision"].String != want || len(plain.delivery.Object["replacements"].Array) != 0 {
			tx.Rollback()
			t.Fatalf("%s delivery without approval = intent %q collision %q", policy, plain.delivery.Object["intent"].String, plain.delivery.Object["collision"].String)
		}
	}
	material.config.business.Sections["file_effect"] = json.RawMessage(`{"operation":"copy","collision_policy":"overwrite"}`)
	// One target captured twice is not two approvals.
	if _, err := productionIntent(ctx, tx, c, "automatic_source", "", capture, capture); err == nil {
		tx.Rollback()
		t.Fatal("duplicate approval path accepted")
	}
	tx.Rollback()
	verifyReplacementApprovalLockOrderings(t, db, runtimeDB, path)
	verifyReplacementApprovalParentDrift(t, db, runtimeDB, path)
	if _, err := db.Exec(`UPDATE placements SET version=version+1 WHERE id='placement'`); err != nil {
		t.Fatal(err)
	}
	tx, err = runtimeDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := capture.recheck(ctx, tx, "release", "anime-1"); err == nil {
		t.Fatal("stale placement accepted")
	}
	if _, err := capture.recheck(ctx, tx, "release", "other-owner"); err == nil {
		t.Fatal("cross-owner approval accepted")
	}
}
