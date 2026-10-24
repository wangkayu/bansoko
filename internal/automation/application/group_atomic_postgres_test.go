package application

import (
	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/workitems"
	"context"
	"testing"
	"time"
)

func TestForcedRefreshProvisionFailureKeepsPriorScopeAndSnapshot(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,manual_selected,effective_selected,effective_score,count_in_limit,available,availability_generation) VALUES('old','sub-1','anibt','old','Old','manual',TRUE,TRUE,0,FALSE,TRUE,7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).Exec(`CREATE FUNCTION test_group_atomic_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected group source failure'; END $$; CREATE TRIGGER test_group_atomic_fail BEFORE INSERT ON rss_sources FOR EACH ROW EXECUTE FUNCTION test_group_atomic_fail()`); err != nil {
		t.Fatal(err)
	}
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "new")
	now := time.Now().UTC()
	active := true
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", ObservedAt: now, SchemaVersion: "test-groups-v1", Complete: true, CompletenessEvidence: "complete-test", Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "New", ObservedAt: now, Active: &active, FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Group: ref}}}}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := Dispatcher{DB: db, WorkItems: store, GroupReaders: map[string]GroupReader{"anibt": staticDispatcherGroupReader{result: result}}, Connections: dispatcherTestConnections{{Kind: configurationdomain.ConnectionAniBT, Endpoint: "https://anibt.example", State: configurationdomain.ConnectionConfigured}}}
	got, err := d.DiscoverGroups(ctx, "anime-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "provider_error" {
		t.Fatalf("outcome=%+v", got)
	}
	var available, manual, selected bool
	var generation int64
	if err := db.QueryRow(`SELECT available,manual_selected,effective_selected,availability_generation FROM provider_group_selections WHERE id='old'`).Scan(&available, &manual, &selected, &generation); err != nil {
		t.Fatal(err)
	}
	if !available || !manual || !selected || generation != 7 {
		t.Fatalf("prior scope partially retired: %t/%t/%t/%d", available, manual, selected, generation)
	}
	for _, table := range []string{"provider_group_snapshots", "rss_sources", "work_items", "provider_group_scope_generations"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained partial write: %d", table, count)
		}
	}
}
