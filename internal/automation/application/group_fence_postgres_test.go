package application

import (
	providerdomain "bansoko/internal/provider/domain"
	"context"
	"testing"
	"time"
)

type changingGroupReader struct {
	beforeReturn func()
	result       providerdomain.GroupReadResult
}

func (r changingGroupReader) ReadGroups(context.Context, string) (providerdomain.GroupReadResult, error) {
	r.beforeReturn()
	return r.result, nil
}

func TestForcedDiscoveryRejectsStaleCompleteResponse(t *testing.T) {
	for _, change := range []string{"generation", "subscription", "owner"} {
		t.Run(change, func(t *testing.T) {
			db := openDispatcherTestDB(t)
			insertProviderSubscription(t, db)
			ctx := context.Background()
			if _, err := db.Exec(`INSERT INTO provider_group_scope_generations(subscription_id,provider,generation) VALUES('sub-1','anibt',1)`); err != nil {
				t.Fatal(err)
			}
			active := true
			ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "old")
			now := time.Now().UTC()
			reader := changingGroupReader{result: providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", ObservedAt: now, SchemaVersion: "test-v1", Complete: true, CompletenessEvidence: "test-complete", Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "Old", Active: &active, ObservedAt: now, FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Group: ref}}}}, beforeReturn: func() {
				query := `UPDATE provider_group_scope_generations SET generation=2 WHERE subscription_id='sub-1'`
				if change == "subscription" {
					query = `UPDATE subscriptions SET version=version+1 WHERE id='sub-1'`
				}
				if change == "owner" {
					query = `UPDATE anime_business_configs SET subgroups_version=subgroups_version+1 WHERE managed_anime_id='anime-1'`
				}
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,count_in_limit,available,availability_generation) VALUES('new','sub-1','anibt','new','New','auto',TRUE,50,TRUE,TRUE,2)`); err != nil {
					t.Fatal(err)
				}
			}}
			d := Dispatcher{DB: db, GroupReaders: map[string]GroupReader{"anibt": reader}}
			if _, err := d.discoverOneMode(ctx, "anime-1", "sub-1", "anibt", "123", "Show", discoveryPolicy{MaxRSS: 3}, true); err == nil {
				t.Fatal("stale complete response accepted")
			}
			var available bool
			var generation int64
			if err := db.QueryRow(`SELECT available,availability_generation FROM provider_group_selections WHERE id='new'`).Scan(&available, &generation); err != nil {
				t.Fatal(err)
			}
			if !available || generation != 2 {
				t.Fatalf("new group retired: %t/%d", available, generation)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM provider_group_snapshots`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("stale snapshot committed")
			}
		})
	}
}
