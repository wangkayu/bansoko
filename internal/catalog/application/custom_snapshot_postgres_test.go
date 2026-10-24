package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/catalog/adapter/persistence"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"context"
	"errors"
	"testing"
)

type customSuccessFetcher struct {
	calls int
	after func()
}

func (f *customSuccessFetcher) FetchCustomSnapshot(context.Context, string) ([]acquisitiondomain.FeedItem, error) {
	f.calls++
	if f.after != nil {
		f.after()
	}
	return feedItems(), nil
}

type customCASReader struct {
	source  FrozenCustomSnapshotSource
	changed bool
}

func (r *customCASReader) ResolveCustomSnapshotSource(context.Context, string, int64, int64) (FrozenCustomSnapshotSource, error) {
	if r.changed {
		return FrozenCustomSnapshotSource{}, errors.New("candidate version changed")
	}
	return r.source, nil
}

// Port-level validator only; real candidate CAS authority is the readmodel pipeline PG.
func (r *customCASReader) ValidateCustomSnapshotSourceTx(ctx context.Context, _ txscope.Tx, id string, revision, version int64, expected FrozenCustomSnapshotSource) error {
	current, err := r.ResolveCustomSnapshotSource(ctx, id, revision, version)
	if err != nil {
		return err
	}
	if current != expected {
		return errors.New("scope changed")
	}
	return nil
}
func TestPGCustomFetchPersistsExactSnapshotAndRechecksCAS(t *testing.T) {
	db := openCatalogTestDB(t)
	defer db.Close()
	ctx := context.Background()
	for _, changed := range []bool{false, true} {
		name := "persist_restart_read"
		if changed {
			name = "changed_after_fetch"
		}
		t.Run(name, func(t *testing.T) {
			entry, group := CustomSnapshotScope("candidate-"+name+"#1", "https://example.test/feed", "Only Group")
			reader := &customCASReader{source: FrozenCustomSnapshotSource{Identity: entry, GroupRef: group, GroupName: "Only Group", Locator: "https://example.test/feed"}}
			fetcher := &customSuccessFetcher{}
			if changed {
				fetcher.after = func() { reader.changed = true }
			}
			app := CustomSnapshotApplication{Revisions: reader, Fetcher: fetcher, Snapshots: persistence.NewRepository(db)}
			result, err := app.Fetch(ctx, "candidate-"+name, 1, 1)
			if changed {
				if err == nil {
					t.Fatal("CAS race accepted")
				}
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM provider_feed_snapshots WHERE provider_entry_ref=$1`, entry).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatal("CAS race persisted snapshot")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if fetcher.calls != 1 {
				t.Fatalf("fetch count=%d", fetcher.calls)
			}
			_ = group // The production mapper's stable group identity is checked by readmodel integration.
			restarted := persistence.NewRepository(db)
			snapshot, err := restarted.ReadProviderFeedSnapshotByID(ctx, result.SnapshotID)
			if err != nil {
				t.Fatal(err)
			}
			if err := snapshot.VerifyIdentity(catalogdomain.ProviderRSSPreviewIdentity{SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash}); err != nil {
				t.Fatal(err)
			}
			if snapshot.Provider != "custom" || snapshot.ProviderEntryRef != entry || len(snapshot.Seeds) == 0 {
				t.Fatalf("wrong persisted snapshot %+v", snapshot)
			}
			if fetcher.calls != 1 {
				t.Fatal("restart read fetched again")
			}
		})
	}
}
