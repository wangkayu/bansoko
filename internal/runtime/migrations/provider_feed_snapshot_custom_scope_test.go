package migrations

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A custom feed snapshot is scoped by opaque custom-source and custom-group
// digests; provider-numeric references are refused by the scope CHECK. (A
// positive insert needs the canonical content hash of real seeds, which the
// feed snapshot tests cover.)
func TestProviderFeedSnapshotCustomScope(t *testing.T) {
	db := installedDB(t)
	const insert = `INSERT INTO provider_feed_snapshots(id,provider,provider_entry_ref,provider_group_ref,version,fetched_at,content_hash,seed_count,projection_schema)
VALUES($1,'custom',$2,$3,1,CURRENT_TIMESTAMP,$4,0,'torrent-manifest-v1')`
	hash := "sha256:" + strings.Repeat("a", 64)

	_, err := db.Exec(insert, "invalid-custom", "123", "456", hash)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "provider_feed_snapshots_custom_scope_check" {
		t.Fatalf("custom numeric scope must fail the scope CHECK, got %v", err)
	}
}
