package migrations

import (
	"context"
	"database/sql"
	"testing"
)

func seedSubscriptionAndSource(t *testing.T, db *sql.DB, sourceID, kind, locator string, providerFields string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO managed_anime(id, title) VALUES ('anime-src', 'Source identity')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state)
		VALUES ('sub-src', 'anime-src', 'custom', NULL, 'Source identity', 'active')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	statement := `
		INSERT INTO rss_sources
			(id, subscription_id, source_kind, locator, locator_display, group_name, title,
			 provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version, provenance)
		VALUES ($1, 'sub-src', $2, $3, $3, 'Group', 'Source', ` + providerFields + `)`
	if _, err := db.ExecContext(ctx, statement, sourceID, kind, locator); err != nil {
		t.Fatalf("seed source %s: %v", sourceID, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, sourceID); err != nil {
		t.Fatalf("seed runtime %s: %v", sourceID, err)
	}
}

func providerValues(provider, entry, group, snapshot string) string {
	return "'" + provider + "', '" + entry + "', '" + group + "', '" + snapshot + "', jsonb_build_object(" +
		"'provider', '" + provider + "', 'provider_entry_ref', '" + entry + "', 'provider_group_ref', '" + group + "', 'discovery_snapshot_version', '" + snapshot + "')"
}

func TestRSSSourceIdentityUniqueness(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	// A provider and custom source may reuse a locator, while custom locators
	// remain unique within a subscription and provider identities remain unique.
	seedSubscriptionAndSource(t, db, "provider-src", "provider_group", "https://same.example/feed", providerValues("mikan", "42", "7", "snap-1"))
	runner := Runner{DB: db, Dir: migrationsDir(t)}
	if err := runner.Verify(ctx); err != nil {
		t.Fatalf("verify installed schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
		VALUES ('custom-src', 'sub-src', 'custom_rss', 'https://same.example/feed', 'https://same.example/feed', 'Group', 'Source')`); err != nil {
		t.Fatalf("provider and custom sources must be allowed to share a locator: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('custom-src')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
		VALUES ('custom-src-duplicate', 'sub-src', 'custom_rss', 'https://same.example/feed', 'https://same.example/feed', 'Group', 'Source')`); err == nil {
		t.Fatal("duplicate custom locator must be rejected")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO rss_sources
			(id, subscription_id, source_kind, locator, locator_display, group_name, title,
			 provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version, provenance)
		VALUES ('provider-src-duplicate', 'sub-src', 'provider_group', 'https://other.example/feed', 'https://other.example/feed', 'Group', 'Source',
			'mikan', '42', '7', 'snap-2', jsonb_build_object('provider', 'mikan', 'provider_entry_ref', '42', 'provider_group_ref', '7', 'discovery_snapshot_version', 'snap-2'))`); err == nil {
		t.Fatal("duplicate provider identity must be rejected")
	}

	for _, index := range []string{"rss_sources_provider_identity_uq", "rss_sources_custom_locator_uq"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("index %s is missing", index)
		}
	}
	var oldExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('rss_sources_locator_uq') IS NOT NULL`).Scan(&oldExists); err != nil {
		t.Fatal(err)
	}
	if oldExists {
		t.Fatal("old locator index still exists")
	}

	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatalf("second verify: %v", err)
	}
}
