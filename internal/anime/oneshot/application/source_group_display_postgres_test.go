package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/disposabledb"
)

// A provider seed names its group by identity only. The display name has to come
// from the group observation discovery recorded for that entry — the same read a
// normal provider subscription's rss_sources.group_name comes from — and never
// from the identity itself.
func TestSourceGroupDisplayNameReadsTheRecordedObservationPostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchemaAsOwner(t)
	ctx := context.Background()
	for _, stmt := range []string{
		// The newest observation of entry-1 no longer carries the group, so a
		// name an older observation recorded must not answer for it.
		`INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version,entries)
		 VALUES('snap-2','mikan','entry-1',CURRENT_TIMESTAMP,'mikan-detail-groups-v1',0,2,'[]')`,
		`INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version,entries)
		 VALUES('snap-1','mikan','entry-1',CURRENT_TIMESTAMP,'mikan-detail-groups-v1',1,1,
		 '[{"group_ref":"612","display_name":"LoliHouse","feed_locator":{},"observed_at":"2026-10-05T00:00:00Z"}]')`,
		`INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version,entries)
		 VALUES('snap-3','mikan','entry-2',CURRENT_TIMESTAMP,'mikan-detail-groups-v1',1,1,
		 '[{"group_ref":"612","display_name":"LoliHouse","feed_locator":{},"observed_at":"2026-10-05T00:00:00Z"}]')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	draft := animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginMikan}

	seed := SelectedSeed{Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "entry-2", ProviderGroupRef: "612"}
	name, err := sourceGroupDisplayName(ctx, tx, draft, seed)
	if err != nil || name != "LoliHouse" {
		t.Fatalf("provider seed display name = %q err %v, want the recorded display name", name, err)
	}
	if name == seed.ProviderGroupRef {
		t.Fatal("provider seed display name is the provider group identity")
	}

	// A group the entry's newest observation no longer lists has no recorded
	// name, and the identity is never substituted for one: the admission refuses.
	stale := SelectedSeed{Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "entry-1", ProviderGroupRef: "612"}
	if name, err := recordedSourceGroupName(ctx, tx, string(stale.Provider), stale.ProviderEntryRef, stale.ProviderGroupRef); err != nil || name != "" {
		t.Fatalf("superseded group = %q err %v, want no name", name, err)
	}
	_, err = sourceGroupDisplayName(ctx, tx, draft, stale)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "SOURCE_GROUP_UNAVAILABLE" {
		t.Fatalf("superseded group admission error = %v, want SOURCE_GROUP_UNAVAILABLE", err)
	}
}
