package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	identitymetadataport "bansoko/internal/identitymetadata/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func TestRepositoryUpsertGetRoundTripUpdatesLastUsedAndCopiesBytes(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	fetchedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	entry := identitymetadataport.PosterCacheEntry{
		BGMSubjectID: "583729",
		ImageBytes:   []byte{0x01, 0x02, 0x03, 0x04},
		ContentType:  " image/webp ",
		ImageURL:     " https://images.example.test/583729.webp ",
		FetchedAt:    fetchedAt,
		ExpiresAt:    fetchedAt.Add(2 * time.Hour),
		LastUsed:     fetchedAt.Add(time.Minute),
	}
	wantBytes := append([]byte(nil), entry.ImageBytes...)
	if err := repository.Upsert(ctx, entry); err != nil {
		t.Fatalf("upsert poster cache entry: %v", err)
	}
	entry.ImageBytes[0] = 0xff

	got, found, err := repository.Get(ctx, entry.BGMSubjectID)
	if err != nil {
		t.Fatalf("get poster cache entry: %v", err)
	}
	if !found {
		t.Fatal("upserted poster cache entry was not found")
	}
	if got.BGMSubjectID != entry.BGMSubjectID || got.ContentType != "image/webp" || got.ImageURL != "https://images.example.test/583729.webp" {
		t.Fatalf("poster cache metadata = %+v", got)
	}
	if !got.FetchedAt.Equal(entry.FetchedAt) || !got.ExpiresAt.Equal(entry.ExpiresAt) {
		t.Fatalf("poster cache timestamps = fetched %s expires %s, want fetched %s expires %s", got.FetchedAt, got.ExpiresAt, entry.FetchedAt, entry.ExpiresAt)
	}
	if !bytes.Equal(got.ImageBytes, wantBytes) {
		t.Fatalf("poster cache bytes = %x, want %x", got.ImageBytes, wantBytes)
	}
	if !got.LastUsed.After(entry.LastUsed) {
		t.Fatalf("get did not update last_used: got %s, previous %s", got.LastUsed, entry.LastUsed)
	}

	got.ImageBytes[0] = 0xee
	reread, found, err := repository.Get(ctx, entry.BGMSubjectID)
	if err != nil {
		t.Fatalf("re-read poster cache entry: %v", err)
	}
	if !found {
		t.Fatal("poster cache entry disappeared after re-read")
	}
	if !bytes.Equal(reread.ImageBytes, wantBytes) {
		t.Fatalf("mutating returned bytes changed stored bytes: got %x, want %x", reread.ImageBytes, wantBytes)
	}
}

func TestRepositoryPurgeExpiredPosterCacheRespectsBatchAndKeepsFreshRows(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	now := time.Now().UTC()

	fixtures := []struct {
		id        string
		expiresAt time.Time
	}{
		{id: "expired-oldest", expiresAt: now.Add(-3 * time.Hour)},
		{id: "expired-middle", expiresAt: now.Add(-2 * time.Hour)},
		{id: "expired-newest", expiresAt: now.Add(-time.Hour)},
		{id: "fresh", expiresAt: now.Add(time.Hour)},
	}
	for _, fixture := range fixtures {
		fetchedAt := fixture.expiresAt.Add(-time.Minute)
		if err := repository.Upsert(ctx, identitymetadataport.PosterCacheEntry{
			BGMSubjectID: fixture.id,
			ImageBytes:   []byte(fixture.id),
			ContentType:  "image/jpeg",
			ImageURL:     "https://images.example.test/" + fixture.id + ".jpg",
			FetchedAt:    fetchedAt,
			ExpiresAt:    fixture.expiresAt,
			LastUsed:     fetchedAt,
		}); err != nil {
			t.Fatalf("seed %s: %v", fixture.id, err)
		}
	}

	purge := func(batchSize int) int64 {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin purge transaction: %v", err)
		}
		defer tx.Rollback()
		count, err := repository.PurgeExpired(ctx, txscope.Wrap(tx), batchSize)
		if err != nil {
			t.Fatalf("purge expired poster cache: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit purge transaction: %v", err)
		}
		return count
	}

	if got := purge(2); got != 2 {
		t.Fatalf("first purge deleted %d rows, want 2", got)
	}
	assertPosterCacheRows(t, ctx, db, map[string]int{
		"expired-oldest": 0,
		"expired-middle": 0,
		"expired-newest": 1,
		"fresh":          1,
	})
	if got := purge(2); got != 1 {
		t.Fatalf("second purge deleted %d rows, want 1", got)
	}
	if got := purge(2); got != 0 {
		t.Fatalf("third purge deleted %d rows, want 0", got)
	}
	assertPosterCacheRows(t, ctx, db, map[string]int{
		"expired-oldest": 0,
		"expired-middle": 0,
		"expired-newest": 0,
		"fresh":          1,
	})
}

func TestRepositoryRejectsInvalidPosterRetentionArguments(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repository := NewRepository(db)
	ctx := context.Background()

	for _, batchSize := range []int{0, maxRetentionBatchSize + 1} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin validation transaction: %v", err)
		}
		if _, err := repository.PurgeExpired(ctx, txscope.Wrap(tx), batchSize); err == nil {
			t.Fatalf("batch size %d unexpectedly succeeded", batchSize)
		}
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			t.Fatalf("rollback validation transaction: %v", err)
		}
	}
	if _, err := repository.PurgeExpired(ctx, txscope.Wrap(nil), 1); err == nil {
		t.Fatal("nil transaction scope unexpectedly succeeded")
	}
}

func assertPosterCacheRows(t *testing.T, ctx context.Context, db *sql.DB, want map[string]int) {
	t.Helper()
	for id, expected := range want {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_poster_cache WHERE bgm_id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("count poster cache row %s: %v", id, err)
		}
		if got != expected {
			t.Errorf("poster cache row %s count = %d, want %d", id, got, expected)
		}
	}
}
