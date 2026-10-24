package migrations

import "testing"

// seedRSSSubscription arranges one anime with a custom subscription, the
// parent every RSS source hangs off.
func seedRSSSubscription(p constraintProbe) {
	p.t.Helper()
	p.accept("managed anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Title')`)
	p.accept("subscription", `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-1', 'anime-1', 'custom', 'Custom', 'active')`)
}

// A custom RSS source keeps its opaque locator, defaults its offset to zero and
// bounds the offset to the allowed range.
func TestRSSSourceConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	seedRSSSubscription(p)
	p.accept("opaque custom locator", `
INSERT INTO rss_sources(
    id, subscription_id, source_kind, locator, locator_display, group_name, title
) VALUES ('src-1', 'sub-1', 'custom_rss', 'ftp://user:pass@feeds.example/rss.xml', 'ftp://feeds.example/rss.xml', 'Custom', 'Title')`)
	p.accept("runtime row", `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('src-1')`)
	var defaultOffset int
	if err := p.db.QueryRowContext(p.ctx, `SELECT episode_offset FROM rss_sources WHERE id = 'src-1'`).Scan(&defaultOffset); err != nil || defaultOffset != 0 {
		t.Fatalf("link episode_offset default = %d (%v), want 0", defaultOffset, err)
	}
	p.reject("null link episode offset",
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, episode_offset) VALUES ('src-null-offset', 'sub-1', 'custom_rss', 'custom://null-offset', 'custom://null-offset', 'Group', 'Title', NULL)`)
	p.reject("out-of-range link episode offset",
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, episode_offset) VALUES ('src-big-offset', 'sub-1', 'custom_rss', 'custom://big-offset', 'custom://big-offset', 'Group', 'Title', 1001)`)
}

// Provider provenance is all-or-nothing and belongs to provider-group sources.
func TestRSSSourceProviderProvenance(t *testing.T) {
	p := newConstraintProbe(t)
	seedRSSSubscription(p)
	p.accept("complete provider provenance", `
INSERT INTO rss_sources(
    id, subscription_id, source_kind, locator, locator_display, group_name, title,
    episode_offset, provenance
) VALUES (
    'src-2', 'sub-1', 'provider_group', 'provider://opaque', 'provider://opaque', 'Group', 'Title', 0,
    '{"provider":"mikan","provider_entry_ref":"entry-1","provider_group_ref":"group-1","discovery_snapshot_version":"snapshot-1"}'::jsonb
)`)
	var provider, entryRef, groupRef, snapshotVersion string
	if err := p.db.QueryRowContext(p.ctx, `
SELECT provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version
FROM rss_sources WHERE id = 'src-2'`).Scan(&provider, &entryRef, &groupRef, &snapshotVersion); err != nil {
		t.Fatal(err)
	}
	if provider != "mikan" || entryRef != "entry-1" || groupRef != "group-1" || snapshotVersion != "snapshot-1" {
		t.Fatalf("unexpected synced provider provenance: %q %q %q %q", provider, entryRef, groupRef, snapshotVersion)
	}
	p.reject("partial provider provenance",
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, provenance) VALUES ('src-3', 'sub-1', 'provider_group', 'provider://partial', 'provider://partial', 'Group', 'Title', '{"provider":"mikan"}'::jsonb)`)
	p.reject("custom source provider provenance",
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, provenance) VALUES ('src-4', 'sub-1', 'custom_rss', 'custom://with-provenance', 'custom://with-provenance', 'Group', 'Title', '{"provider":"mikan","provider_entry_ref":"entry-1","provider_group_ref":"group-1","discovery_snapshot_version":"snapshot-1"}'::jsonb)`)
}

// An RSS source is identified by its locator, not by legacy URL, offset
// override or enabled columns.
func TestRSSSourceHasNoLegacyColumns(t *testing.T) {
	db := installedDB(t)
	for _, column := range []string{"locator_url", "offset_override", "enabled"} {
		if columnPresent(t, db, "rss_sources", column) {
			t.Fatalf("legacy RSS source column exists: %s", column)
		}
	}
}
