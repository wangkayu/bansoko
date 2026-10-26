package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/disposabledb"
)

// Migration 059: Nyaa is a torrent-search source only. A search snapshot may
// carry it; a provider feed snapshot (the discovery kind) may not, in the table
// itself.
func TestNyaaIsStoredOnlyAsASearchSnapshot(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	now := time.Now().UTC()

	search := persistSnapshotKind(t, repository, "nyaa",
		"search-scope:"+strings.Repeat("1", 64), "search-query:"+strings.Repeat("2", 64),
		catalogdomain.FeedSnapshotKindSearch, now)
	read, err := repository.ReadProviderFeedSnapshotByID(ctx, search.ID)
	if err != nil {
		t.Fatalf("read nyaa search snapshot: %v", err)
	}
	if read.Provider != "nyaa" || read.SnapshotKind != catalogdomain.FeedSnapshotKindSearch {
		t.Fatalf("round trip = %s/%s, want nyaa/search", read.Provider, read.SnapshotKind)
	}

	feed := persistSnapshotKind(t, repository, "anibt", "entry-1", "group-1", catalogdomain.FeedSnapshotKindProviderFeed, now)
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshots SET provider = 'nyaa' WHERE id = $1`, feed.ID); err == nil {
		t.Fatal("a provider feed snapshot accepted the nyaa provider")
	}
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshots SET provider = 'bogus' WHERE id = $1`, search.ID); err == nil {
		t.Fatal("a search snapshot accepted an unknown provider")
	}
}
