package migrations

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	persistence "bansoko/internal/catalog/adapter/persistence"
	domain "bansoko/internal/catalog/domain"
	"bansoko/internal/runtime/database"
)

func publishFeedFixture(t *testing.T, db *sql.DB) domain.ProviderFeedSnapshot {
	t.Helper()
	payload := []byte("frozen feed torrent bytes")
	digest := domain.DigestBytes(payload)
	size := int64(len(payload))
	now := time.Now().UTC()
	seeds := []domain.ProviderFeedSnapshotSeed{{StableSeedKey: "seed", Locator: "https://example.test/a.torrent", LocatorDisplay: "example.test/a.torrent", Title: "Release", Payload: payload, PayloadDigest: &digest, SizeBytes: &size}}
	hash, err := domain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := persistence.NewRepository(db).PersistProviderFeedSnapshot(context.Background(), domain.ProviderFeedSnapshotWrite{Provider: "mikan", ProviderEntryRef: "123", ProviderGroupRef: "456", FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: hash, HashSchema: domain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: domain.TorrentManifestProjectionSchema, Seeds: seeds})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func TestFeedSnapshotRuntimePublicationAndLeafCapabilities(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	snapshot := publishFeedFixture(t, runtime)
	for _, table := range []string{"provider_feed_snapshots", "provider_feed_snapshot_seeds"} {
		t.Run(table, func(t *testing.T) {
			for _, statement := range []string{"INSERT INTO " + table + " DEFAULT VALUES", "TRUNCATE " + table + " CASCADE"} {
				if _, err := runtime.ExecContext(ctx, statement); sqlState(err) != "42501" {
					t.Fatalf("raw write capability: %s %v", statement, err)
				}
			}
			if table != "provider_feed_snapshots" {
				if _, err := runtime.ExecContext(ctx, "DELETE FROM "+table); sqlState(err) != "42501" {
					t.Fatalf("direct child delete: %v", err)
				}
			}
		})
	}
	if _, err := runtime.ExecContext(ctx, `UPDATE provider_feed_snapshot_seeds SET payload=payload`); sqlState(err) != "42501" {
		t.Fatalf("direct seed payload update: %v", err)
	}
	if _, err := runtime.ExecContext(ctx, `UPDATE provider_feed_snapshots SET seed_count=seed_count`); sqlState(err) != "42501" {
		t.Fatalf("direct header content: %v", err)
	}
	if _, err := runtime.ExecContext(ctx, `UPDATE provider_feed_snapshots SET fresh_until=fetched_at,retention_until=created_at WHERE id=$1`, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE id=$1`, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM provider_feed_snapshot_seeds`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("whole parent GC did not cascade: %d %v", count, err)
	}
}
func TestFeedSnapshotFinalSealRejectsPartialGraph(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, extra, message string }{
		{"missing seed", "", "FEED_SNAPSHOT_SEED_COUNT"},
		{"insert then delete", `DELETE FROM provider_feed_snapshots WHERE id='partial'`, "FEED_SNAPSHOT_FINAL_MISSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := inTx(db, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `INSERT INTO provider_feed_snapshots(id,provider,provider_entry_ref,provider_group_ref,version,content_hash,hash_schema,projection_schema,seed_count) VALUES('partial','mikan','123','456',1,$1,'provider-feed-snapshot-v1','torrent-manifest-v1',1)`, acqDigest("a")); err != nil {
					return err
				}
				if tc.extra != "" {
					_, err := tx.ExecContext(ctx, tc.extra)
					return err
				}
				return nil
			})
			if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != tc.message {
				t.Fatalf("partial graph seal: %v", err)
			}
		})
	}
}
func TestFeedSnapshotParentShareLocksFeedGCAndFreshness(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, statement := range []string{`UPDATE provider_feed_snapshots SET fresh_until=fetched_at WHERE id=$1`, `DELETE FROM provider_feed_snapshots WHERE id=$1`} {
		name := "expire"
		if strings.HasPrefix(statement, "DELETE") {
			name = "GC"
		}
		t.Run(name, func(t *testing.T) {
			snapshot := publishFeedFixture(t, db)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var hash string
			if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM provider_feed_snapshots WHERE id=$1 FOR SHARE`, snapshot.ID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var pid int
			if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := conn.ExecContext(callCtx, statement, snapshot.ID); done <- err }()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				if err := db.QueryRowContext(callCtx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("writer bypassed parent share: %v", err)
				case <-callCtx.Done():
					t.Fatal("writer never reached lock")
				case <-ticker.C:
				}
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM provider_feed_snapshot_seeds WHERE snapshot_id=$1`, snapshot.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("child changed while locked: %d %v", count, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
