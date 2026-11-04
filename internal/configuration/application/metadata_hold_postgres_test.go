package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/workitems"
)

// Turning the Global release-without-metadata switch on re-enqueues the
// releases already held as waiting_metadata in the same save transaction, so
// they are admitted without waiting for their next metadata event. Saving the
// switch off never touches a release, and saving it on again adds no second
// live item.
func TestGlobalMetadataHoldSaveEnqueuesWaitingReleases(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seedSiteInstance(t, ctx, db, "site-hold", "site-hold-sub", "active")
	for _, stmt := range []string{
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title,
		                        provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version, provenance)
		 VALUES ('site-hold-alpha', 'site-hold-sub', 'provider_group', 'https://feeds.example.test/alpha.xml', 'https://feeds.example.test/alpha.xml', 'Alpha', 'Site',
		         'anibt', '777', 'alpha', 'v1', '{"provider":"anibt","provider_entry_ref":"777","provider_group_ref":"alpha","discovery_snapshot_version":"v1"}'::jsonb)`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts, verdict, reason_code)
		 VALUES ('site-held-release', 'site-hold-alpha', 'guid:site-held-release', '{"title":"[Alpha] Site - 01"}', '{}', '{"title":"[Alpha] Site - 01"}', 'waiting_metadata', 'metadata_incomplete')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, newConnectionTestSecretStore(t, db))
	svc.SetWorkItems(store)
	schema := animedomain.MetadataHoldSchemaVersion

	save := func(expected int64, allowed bool) configurationdomain.GlobalSectionView {
		t.Helper()
		view, err := svc.PutGlobalSection(ctx, animedomain.SectionMetadataHold, expected, configurationdomain.GlobalSectionInput{
			SchemaVersion: schema,
			Value:         map[string]interface{}{"allow_missing_linkage": allowed},
		})
		if err != nil {
			t.Fatalf("save metadata_hold: %v", err)
		}
		return view
	}
	liveItems := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_items
WHERE owner_type = 'release' AND owner_id = 'site-held-release' AND kind = 'release_evaluate'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	off := save(0, false)
	if off.Version != 1 || liveItems() != 0 {
		t.Fatalf("switch off version=%d items=%d, want version 1 and no re-evaluation", off.Version, liveItems())
	}
	on := save(off.Version, true)
	if liveItems() != 1 {
		t.Fatalf("switch on enqueued %d items, want 1", liveItems())
	}
	var snapshot []byte
	if err := db.QueryRowContext(ctx, `
SELECT input_snapshot FROM work_items
WHERE owner_type = 'release' AND owner_id = 'site-held-release' AND kind = 'release_evaluate'`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["trigger"] != "metadata_release_allowed" {
		t.Fatalf("re-evaluation trigger = %q, want metadata_release_allowed", decoded["trigger"])
	}
	save(on.Version, true)
	if liveItems() != 1 {
		t.Fatalf("saving the switch on again added a second item: %d", liveItems())
	}
}

// A switch flip has no release to touch when nothing is waiting: the save
// still writes the section and enqueues nothing.
func TestGlobalMetadataHoldSaveWithoutWaitingReleasesWritesOnlyTheSection(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, newConnectionTestSecretStore(t, db))
	svc.SetWorkItems(store)
	if _, err := svc.PutGlobalSection(ctx, animedomain.SectionMetadataHold, 0, configurationdomain.GlobalSectionInput{
		SchemaVersion: animedomain.MetadataHoldSchemaVersion,
		Value:         map[string]interface{}{"allow_missing_linkage": true},
	}); err != nil {
		t.Fatalf("save metadata_hold: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE kind = 'release_evaluate'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("enqueued %d release evaluations without any held release", count)
	}
}
