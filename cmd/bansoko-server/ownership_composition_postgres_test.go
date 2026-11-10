package main

import (
	"context"
	"testing"

	"bansoko/internal/events"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/workitems"
)

type ownershipCompositionRefresher struct{}

func (ownershipCompositionRefresher) RefreshWork(context.Context, string, string, string, int) error {
	return nil
}

// The production provider must connect the same hub consumed by SSE. Without
// this composition test, the application-level event test still passes while
// the real server's ownership service silently constructs a nil-hub invalidator.
func TestProvideOwnershipServicePublishesMetadataRefreshEvents(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	workStore, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	service, err := provideOwnershipService(db, workStore, nil, hub)
	if err != nil {
		t.Fatal(err)
	}
	service.SetMetadataWorkRefresher(ownershipCompositionRefresher{})
	published, cancel := hub.Subscribe()
	defer cancel()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-composition-refresh', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id, bgm_work_id) VALUES ('anime-composition-refresh', '10')`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshAnimeMetadataApplication(ctx, "anime-composition-refresh"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-published:
		if event.Target != events.TargetRSS || event.ManagedAnimeID != "anime-composition-refresh" {
			t.Fatalf("production composition publishes the scoped refresh event, got %+v", event)
		}
	default:
		t.Fatal("production ownership composition lost the metadata refresh event")
	}
}
