package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/testutil/animetest"
)

// TestInstanceRetentionSaveKeepsFrozenBusinessConsistent proves ruling 3 for a
// policy-axis single-item PATCH: saving one instance retention policy rewrites
// the frozen business snapshot in the same transaction, so the row never holds
// two disagreeing copies of the instance configuration.
func TestInstanceRetentionSaveKeepsFrozenBusinessConsistent(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	b := frozenBusinessFixture(t)
	draft.Business = b.Business
	draft.BusinessDigest = b.BusinessDigest
	seedBusinessPrograms(t, db, &draft)
	original, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := service.ActivateAnimeConfigCandidate(ctx, original.Id, original.Version, "business-sync-0001", CandidateSubscribeInput{Revision: *original.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	animeID := activated.ManagedAnime.ID

	before := readInstanceBusinessRow(t, db, animeID)
	if before.Snapshot == nil {
		t.Fatal("activation must freeze a business snapshot")
	}
	retention := filemaintenance.NewRetentionPolicyService(db)
	instance, err := retention.GetInstance(ctx, animeID)
	if err != nil || instance == nil {
		t.Fatalf("instance retention policy: row=%v err=%v", instance, err)
	}
	next := filemaintenance.RetentionPolicy{
		AdmissionMode:  filemaintenance.ModeSingleCopy,
		QualityProfile: before.Snapshot.Retention.QualityProfile,
		CleanupPolicy:  before.Snapshot.Retention.CleanupPolicy,
	}
	saved, err := retention.SaveInstance(ctx, animeID, instance.Version, next, "owner", "web")
	if err != nil {
		t.Fatalf("save instance retention policy: %v", err)
	}

	after := readInstanceBusinessRow(t, db, animeID)
	if after.RetentionVersion != saved.Version {
		t.Fatalf("row retention version = %d, want %d", after.RetentionVersion, saved.Version)
	}
	if after.BusinessVersion != before.BusinessVersion+1 {
		t.Fatalf("business version did not advance with the policy save: %d -> %d", before.BusinessVersion, after.BusinessVersion)
	}
	if after.Snapshot.Retention.AdmissionMode != string(filemaintenance.ModeSingleCopy) || after.Snapshot.RetentionVersion != before.Snapshot.RetentionVersion {
		t.Fatalf("snapshot retention did not follow the save: %+v", after.Snapshot.Retention)
	}
	if !reflect.DeepEqual(after.Snapshot.Sections, before.Snapshot.Sections) ||
		after.Snapshot.Parser != before.Snapshot.Parser ||
		after.Snapshot.UnclassifiedRoute != before.Snapshot.UnclassifiedRoute ||
		after.Snapshot.FileEffectVersion != before.Snapshot.FileEffectVersion {
		t.Fatalf("an unrelated axis changed with the retention save: before=%+v after=%+v", before.Snapshot, after.Snapshot)
	}
	digest, err := after.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != after.BusinessDigest {
		t.Fatalf("stored digest %s is not the snapshot digest %s", after.BusinessDigest, digest)
	}
}

type instanceBusinessRow struct {
	RetentionVersion int64
	BusinessVersion  int64
	BusinessDigest   string
	Snapshot         *animedomain.BusinessSnapshot
}

func readInstanceBusinessRow(t *testing.T, db *sql.DB, animeID string) instanceBusinessRow {
	t.Helper()
	var (
		row    instanceBusinessRow
		raw    []byte
		digest sql.NullString
	)
	if err := db.QueryRowContext(context.Background(), `
SELECT retention_version, business_version, business_snapshot, business_digest
FROM anime_business_configs WHERE managed_anime_id=$1`, animeID).
		Scan(&row.RetentionVersion, &row.BusinessVersion, &raw, &digest); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 0 {
		var snapshot animedomain.BusinessSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		row.Snapshot = &snapshot
		row.BusinessDigest = digest.String
	}
	return row
}
