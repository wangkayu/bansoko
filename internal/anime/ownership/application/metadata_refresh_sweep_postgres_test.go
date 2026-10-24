package application

// The metadata refresh sweep's read side over the disposable schema: the
// Global toggle (missing row = off) and the active subscriptions' bound works
// only. The orchestration itself is covered with a fake source in
// metadata_refresh_sweep_test.go.

import (
	"context"
	"strings"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/workitems"
)

func TestMetadataRefreshTargetsSelectsActiveBoundWorks(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anime-active-bound", "anime-active-tmdb", "anime-stopped-bound", "anime-active-unbound"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Show')`, id); err != nil {
			t.Fatal(err)
		}
	}
	fixtures := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO anime_metadata(managed_anime_id, bgm_work_id) VALUES ($1, '10')`, []any{"anime-active-bound"}},
		{`INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season) VALUES ($1, '42', 'tv', 1)`, []any{"anime-active-tmdb"}},
		{`INSERT INTO anime_metadata(managed_anime_id, bgm_work_id) VALUES ($1, '11')`, []any{"anime-stopped-bound"}},
		{`INSERT INTO anime_metadata(managed_anime_id) VALUES ($1)`, []any{"anime-active-unbound"}},
		{`INSERT INTO subscriptions(id, managed_anime_id, origin, state) VALUES ('sub-active', $1, 'mikan', 'active')`, []any{"anime-active-bound"}},
		{`INSERT INTO subscriptions(id, managed_anime_id, origin, state) VALUES ('sub-tmdb', $1, 'mikan', 'active')`, []any{"anime-active-tmdb"}},
		{`INSERT INTO subscriptions(id, managed_anime_id, origin, state) VALUES ('sub-stopped', $1, 'mikan', 'stopped')`, []any{"anime-stopped-bound"}},
		{`INSERT INTO subscriptions(id, managed_anime_id, origin, state) VALUES ('sub-unbound', $1, 'mikan', 'active')`, []any{"anime-active-unbound"}},
	}
	for _, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, fixture.statement, fixture.args...); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}

	targets, err := service.MetadataRefreshTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(targets, ","), "anime-active-bound,anime-active-tmdb"; got != want {
		t.Fatalf("targets = %q, want %q: stopped and unbound works are never refreshed", got, want)
	}
}

func TestMetadataRefreshEnabledReadsTheGlobalToggle(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}

	enabled, err := service.MetadataRefreshEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("a missing row is the default (off)")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('metadata_refresh', 'metadata-refresh-v1', '{"enabled":true}'::jsonb, 1)`); err != nil {
		t.Fatal(err)
	}
	enabled, err = service.MetadataRefreshEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("the saved enabled value is read")
	}

	if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value = '{"enabled":false}'::jsonb, version = version + 1 WHERE section_name = 'metadata_refresh'`); err != nil {
		t.Fatal(err)
	}
	enabled, err = service.MetadataRefreshEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("a later disabled save stops the sweep")
	}
}

// TestMetadataRefreshValueShapeIsCheckedByTheTable pins migration 052's
// value check: exactly one boolean member and the pinned schema version.
func TestMetadataRefreshValueShapeIsCheckedByTheTable(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	for _, value := range []string{
		`{"enabled":false,"extra":1}`,
		`{"enabled":"yes"}`,
		`{}`,
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('metadata_refresh', 'metadata-refresh-v1', $1::jsonb, 1)`, value); err == nil {
			t.Fatalf("%s was accepted by the metadata_refresh check", value)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('metadata_refresh', 'metadata-refresh-v0', '{"enabled":false}'::jsonb, 1)`); err == nil {
		t.Fatal("an unsupported schema version was accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('metadata_refresh', 'metadata-refresh-v1', '{"enabled":false}'::jsonb, 1)`); err != nil {
		t.Fatalf("the typed value was refused: %v", err)
	}
}
