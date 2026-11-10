package persistence

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/testutil/disposabledb"
)

func TestBangumiDataSnapshotRoundTripAndSingleRowUpsert(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	if _, found, err := repository.Load(ctx); err != nil || found {
		t.Fatalf("empty snapshot store must be a normal miss: found=%v err=%v", found, err)
	}

	fetchedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	payload := []byte(`{"items":[{"title":"Show"}]}`)
	if err := repository.Save(ctx, Snapshot{UpstreamVersion: "0.3.234", Payload: payload, LastModified: "old", FetchedAt: fetchedAt}); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := repository.Load(ctx)
	if err != nil || !found || loaded.UpstreamVersion != "0.3.234" || string(loaded.Payload) != string(payload) || !loaded.FetchedAt.Equal(fetchedAt) || !loaded.CheckedAt.Equal(fetchedAt) {
		t.Fatalf("snapshot round trip failed: %+v found=%v err=%v", loaded, found, err)
	}
	loaded.Payload[0] = 'x'
	again, _, err := repository.Load(ctx)
	if err != nil || string(again.Payload) != string(payload) {
		t.Fatal("loaded payload must not alias repository-owned bytes")
	}

	newFetch := fetchedAt.Add(2 * time.Hour)
	if err := repository.Save(ctx, Snapshot{UpstreamVersion: "0.3.235", Payload: []byte(`{"items":[]}`), LastModified: "new", FetchedAt: newFetch}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM bangumi_data_snapshots`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("snapshot upsert must retain one row: count=%d err=%v", count, err)
	}
	checkedAt := newFetch.Add(time.Hour)
	if err := repository.Touch(ctx, checkedAt); err != nil {
		t.Fatal(err)
	}
	loaded, found, err = repository.Load(ctx)
	if err != nil || !found || loaded.UpstreamVersion != "0.3.235" || loaded.LastModified != "new" || !loaded.CheckedAt.Equal(checkedAt) {
		t.Fatalf("updated snapshot was not restored: %+v found=%v err=%v", loaded, found, err)
	}
}
