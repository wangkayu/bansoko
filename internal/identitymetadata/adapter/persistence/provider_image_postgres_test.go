package persistence

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	identitymetadataport "bansoko/internal/identitymetadata/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func TestProviderImageRegisterGetStoreRoundTrip(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewProviderImageRepository(db)
	key := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)

	if err := repository.Register(ctx, "mikan", []identitymetadataport.ProviderImageRef{
		{Key: key, SourceURL: "https://mikanani.me/a.jpg"},
		{Key: key, SourceURL: "https://mikanani.me/a.jpg"}, // duplicate in one batch
		{Key: other, SourceURL: "https://mikanani.me/b.jpg"},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entry, found, err := repository.Get(ctx, key)
	if err != nil || !found {
		t.Fatalf("get registered = %v, %v", found, err)
	}
	if entry.HasBytes || entry.Provider != "mikan" || entry.SourceURL != "https://mikanani.me/a.jpg" {
		t.Fatalf("registered entry = %+v", entry)
	}
	if _, found, err := repository.Get(ctx, strings.Repeat("ef", 32)); err != nil || found {
		t.Fatalf("unknown key = %v, %v", found, err)
	}

	fetchedAt := time.Now().UTC().Truncate(time.Microsecond)
	data := []byte{1, 2, 3, 4}
	if err := repository.Store(ctx, key, data, "image/png", fetchedAt, fetchedAt.Add(time.Hour)); err != nil {
		t.Fatalf("store: %v", err)
	}
	data[0] = 9 // the repository must have copied the bytes
	entry, found, err = repository.Get(ctx, key)
	if err != nil || !found || !entry.HasBytes {
		t.Fatalf("get stored = %v, %v, %+v", found, err, entry)
	}
	if !bytes.Equal(entry.ImageBytes, []byte{1, 2, 3, 4}) || entry.ContentType != "image/png" || !entry.ExpiresAt.Equal(fetchedAt.Add(time.Hour)) {
		t.Fatalf("stored entry = %+v", entry)
	}

	// Registering again keeps the bytes.
	if err := repository.Register(ctx, "mikan", []identitymetadataport.ProviderImageRef{{Key: key, SourceURL: "https://mikanani.me/a.jpg"}}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if entry, _, _ := repository.Get(ctx, key); !entry.HasBytes {
		t.Fatal("re-registering dropped the cached bytes")
	}

	// Rejected shapes never reach the table.
	if err := repository.Register(ctx, "bgm", []identitymetadataport.ProviderImageRef{{Key: strings.Repeat("12", 32), SourceURL: "https://x.example/a.jpg"}}); err == nil {
		t.Fatal("an unknown provider was registered")
	}
	if err := repository.Register(ctx, "mikan", []identitymetadataport.ProviderImageRef{{Key: "not-a-hash", SourceURL: "https://mikanani.me/c.jpg"}}); err == nil {
		t.Fatal("a malformed key was registered")
	}
	if err := repository.Store(ctx, key, bytes.Repeat([]byte{1}, maxProviderImageBytes+1), "image/png", fetchedAt, fetchedAt); err == nil {
		t.Fatal("oversize bytes were stored")
	}
}

func TestPurgeExpiredAlsoDropsIdleProviderImages(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewProviderImageRepository(db)
	idle, fresh := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	if err := repository.Register(ctx, "tmdb", []identitymetadataport.ProviderImageRef{
		{Key: idle, SourceURL: "https://image.tmdb.org/t/p/w342/a.jpg"},
		{Key: fresh, SourceURL: "https://image.tmdb.org/t/p/w342/b.jpg"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE provider_image_cache SET last_used = CURRENT_TIMESTAMP - INTERVAL '31 days' WHERE image_key = $1`, idle); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	count, err := NewRepository(db).PurgeExpired(ctx, txscope.Wrap(tx), 10)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purged %d rows, want 1", count)
	}
	if _, found, _ := repository.Get(ctx, idle); found {
		t.Fatal("idle provider image survived the purge")
	}
	if _, found, _ := repository.Get(ctx, fresh); !found {
		t.Fatal("fresh provider image was purged")
	}
}
