package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func seedMediaFactRelease(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-media', 'Media facts');
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-media', 'anime-media', 'custom', 'Media facts', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-media', 'sub-media', 'custom_rss', 'https://feeds.example.test/media-facts', 'https://feeds.example.test/media-facts', 'G', 'Media facts');
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-media', 'src-media', 'key-media', '{}', '{}', 'discovered', 'discovered')`); err != nil {
		t.Fatalf("seed release chain: %v", err)
	}
}

func insertSnapshot(t *testing.T, db *sql.DB, id, parserConfig string) error {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(
    id, release_id, schema_version, resolver_version, parser_kind,
    parser_config_fingerprint, parser_config, input_digest, release_identity, release_facts
)
VALUES ($1, 'rel-media', 'media-facts-v1', 'resolver-v1', 'regex', $2, $3::jsonb,
        'sha256:input', '{}'::jsonb, '{}'::jsonb)`, id,
		"sha256:"+strings.Repeat("f", 64), parserConfig)
	return err
}

// The frozen parser config on a media fact snapshot is optional (NULL when the
// profile carried no config), a JSON object is accepted and read back, and a
// non-object or oversized config is rejected.
func TestMediaFactSnapshotParserConfig(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedMediaFactRelease(t, db)

	// 1. A row without a frozen config reads back NULL: no invented config.
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(
    id, release_id, schema_version, resolver_version, parser_kind,
    parser_config_fingerprint, input_digest, release_identity, release_facts
)
VALUES ('snap-no-config', 'rel-media', 'media-facts-v1', 'resolver-v1', 'regex',
        NULL, 'sha256:input', '{}'::jsonb, '{}'::jsonb)`); err != nil {
		t.Fatalf("seed snapshot without config: %v", err)
	}
	var stored sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT parser_config::text FROM media_fact_snapshots WHERE id = 'snap-no-config'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Valid {
		t.Fatalf("snapshot received an invented parser config %q", stored.String)
	}

	// 2. A frozen config round-trips unchanged.
	config := `{"episode_patterns":["\\[E(?P<episode>\\d+)\\]"]}`
	if err := insertSnapshot(t, db, "snap-config", config); err != nil {
		t.Fatalf("insert snapshot with parser config: %v", err)
	}
	var semanticEqual bool
	if err := db.QueryRowContext(ctx, `
SELECT parser_config = $1::jsonb FROM media_fact_snapshots WHERE id = 'snap-config'`, config).Scan(&semanticEqual); err != nil {
		t.Fatal(err)
	}
	if !semanticEqual {
		t.Fatalf("parser config round-trip changed its JSON value: %s", config)
	}

	// 3. An empty object (a frozen profile with no patterns is encoded as NULL,
	// but an explicit empty object must still be storable) is accepted.
	if err := insertSnapshot(t, db, "snap-empty", `{}`); err != nil {
		t.Fatalf("insert snapshot with empty parser config: %v", err)
	}

	// 4. A non-object config is rejected by the typed column contract.
	if err := insertSnapshot(t, db, "snap-array", `["not","an","object"]`); err == nil {
		t.Fatal("a non-object parser config must be rejected")
	} else {
		expectPgErrorCode(t, err, "23514")
	}

	// 5. An oversized config is rejected by the bounded-size check. The payload
	// is incompressible so TOAST does not shrink it below the bound.
	if err := insertSnapshot(t, db, "snap-huge", `{"padding":"`+incompressibleString(20000)+`"}`); err == nil {
		t.Fatal("an oversized parser config must be rejected")
	} else {
		expectPgErrorCode(t, err, "23514")
	}
}

func incompressibleString(size int) string {
	var builder strings.Builder
	for builder.Len() < size {
		sum := sha256.Sum256([]byte(fmt.Sprintf("media-fact-%d", builder.Len())))
		builder.WriteString(hex.EncodeToString(sum[:]))
	}
	return builder.String()[:size]
}

// insertFingerprintedSnapshot inserts a media fact snapshot row with an
// explicit parser config fingerprint, so the format constraint can be
// exercised directly.
func insertFingerprintedSnapshot(t *testing.T, db *sql.DB, id string, fingerprint sql.NullString) error {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
INSERT INTO media_fact_snapshots(
    id, release_id, schema_version, resolver_version, parser_kind,
    parser_config_fingerprint, input_digest, release_identity, release_facts
)
VALUES ($1, 'rel-media', 'media-facts-v1', 'resolver-v1', 'regex', $2,
        'sha256:input', '{}'::jsonb, '{}'::jsonb)`, id, fingerprint)
	return err
}

// The frozen parser config fingerprint is the canonical digest shape
// (sha256:<64 lowercase hex>) or NULL; non-conforming writes fail closed at the
// database boundary.
func TestMediaFactSnapshotFingerprintFormat(t *testing.T) {
	db := installedDB(t)
	seedMediaFactRelease(t, db)

	// 1. A well-formed digest is accepted.
	if err := insertFingerprintedSnapshot(t, db, "snap-fp-ok", sql.NullString{String: "sha256:" + strings.Repeat("f", 64), Valid: true}); err != nil {
		t.Fatalf("insert well-formed fingerprint: %v", err)
	}

	// 2. NULL stays admissible (empty profiles).
	if err := insertFingerprintedSnapshot(t, db, "snap-fp-null", sql.NullString{}); err != nil {
		t.Fatalf("insert NULL fingerprint: %v", err)
	}

	// 3. A malformed fingerprint fails closed at the boundary.
	for name, fingerprint := range map[string]string{
		"uppercase hex":  "sha256:" + strings.Repeat("F", 64),
		"missing prefix": strings.Repeat("f", 64),
		"short digest":   "sha256:abcd",
		"empty":          "",
		"free text":      "sha256:not-a-digest",
	} {
		if err := insertFingerprintedSnapshot(t, db, "snap-fp-"+name, sql.NullString{String: fingerprint, Valid: true}); err == nil {
			t.Errorf("%s fingerprint must be rejected", name)
		} else {
			expectPgErrorCode(t, err, "23514")
		}
	}
}
