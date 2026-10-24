package application

import (
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/persistence/txscope"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPGPreparedPlanHandlerBindingRepairAndIdempotency(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		policy["fixture_manifest"] = []map[string]any{
			{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "subtitle", "ordinal": 1, "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
		}
	})
	s := newFixtureService(t, db, f)
	ctx := context.Background()
	// addArtifactFile hashes the actual source; create it before admission.
	if err := os.MkdirAll(filepath.Dir(f.sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.sourcePath+".ass", []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "prepared-sub", "subtitle", "source.chs.ass", f.sourcePath+".ass", "subtitle", 8)
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim := claimPlanWorkItem(t, db, "prepared-binding")
	if _, err := s.PostProcessingPlanHandler().Handle(ctx, claim); err != nil {
		t.Fatal(err)
	}
	rows := loadOperations(t, db, f.artifactID)
	video := operationForFile(t, rows, f.fileID)
	sub := operationForFile(t, rows, "prepared-sub")
	if sub.boundVideoOperationID.String != video.id || sub.state != ownerStatePending {
		t.Fatalf("binding repair lost target: %+v", sub)
	}
	// Independent confirmation observes sealed input; it never replays the writer.
	before := planGraphSnapshot(t, db)
	for i := 0; i < 3; i++ {
		_, ok, err := s.confirmTypedPlanCommit(ctx, claim)
		if err != nil || !ok {
			t.Fatalf("confirmation %d: %v ok=%v", i, err, ok)
		}
	}
	if after := planGraphSnapshot(t, db); after != before {
		t.Fatal("idempotent confirmation changed graph")
	}
	replay := loadOperations(t, db, f.artifactID)
	if len(replay) != len(rows) || operationForFile(t, replay, "prepared-sub").id != sub.id || operationForFile(t, replay, f.fileID).id != video.id {
		t.Fatal("idempotent replay recreated operations")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id IN ($1,$2)`, video.id, sub.id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("prepare first continuations=%d", count)
	}
	var subtitleWork int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1`, sub.id).Scan(&subtitleWork); err != nil {
		t.Fatal(err)
	}
	if subtitleWork != 0 {
		t.Fatal("prepare prematurely dispatched dependent subtitle")
	}
}
