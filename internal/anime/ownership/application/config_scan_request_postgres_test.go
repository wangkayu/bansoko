package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// configScanRevision returns the pending config scan request's revision of the
// anime; 0 means no request is pending.
func configScanRevision(t *testing.T, db *sql.DB, animeID string) int64 {
	t.Helper()
	var revision int64
	err := db.QueryRowContext(context.Background(), `SELECT revision FROM config_scan_requests WHERE managed_anime_id = $1`, animeID).Scan(&revision)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func configScanRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM config_scan_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A title-only save requests the scan and schedules no re-evaluation; a season
// save requests it and re-evaluates in the same call. A save that changes
// nothing a path renders requests nothing.
func TestMetadataPatchRequestsConfigScanByRenderRelevance(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "config-scan-meta-0001")
	animeID := subscribed.ManagedAnime.Id

	// The input is the complete owner value: start from the stored one, so a
	// field the test does not mean to change is sent back as it is.
	current := func() MetadataPatchInput {
		t.Helper()
		var title, bgm, tmdb, tmdbKind, kind sql.NullString
		var year, season, tmdbSeason, total sql.NullInt64
		if err := db.QueryRowContext(ctx, `
SELECT ma.title_override, md.bgm_work_id, md.tmdb_work_id, md.tmdb_media_kind, md.tmdb_season,
       md.media_kind_override, md.total_episodes, md.year, md.season
FROM managed_anime ma JOIN anime_metadata md ON md.managed_anime_id = ma.id WHERE ma.id = $1`, animeID).Scan(
			&title, &bgm, &tmdb, &tmdbKind, &tmdbSeason, &kind, &total, &year, &season); err != nil {
			t.Fatal(err)
		}
		in := MetadataPatchInput{}
		if title.Valid {
			in.TitleOverride = &title.String
		}
		if bgm.Valid {
			in.BGM = &BGMBinding{WorkID: bgm.String}
		}
		if tmdb.Valid {
			in.TMDB = &TMDBBinding{WorkID: tmdb.String, MediaKind: tmdbKind.String, Season: int(tmdbSeason.Int64)}
		}
		if kind.Valid {
			in.MediaKindOverride = &kind.String
		}
		if total.Valid {
			n := int(total.Int64)
			in.TotalEpisodes = &n
		}
		if year.Valid {
			y := int(year.Int64)
			in.Year = &y
		}
		if season.Valid {
			s := int(season.Int64)
			in.Season = &s
		}
		return in
	}
	version := int64(1)

	// Unchanged value: nothing to request, nothing to re-evaluate.
	in := current()
	in.ExpectedVersion = version
	out, err := service.PatchAnimeMetadataApplication(ctx, animeID, in)
	if err != nil {
		t.Fatalf("unchanged patch: %v", err)
	}
	version = out.Version
	if rev := configScanRevision(t, db, animeID); rev != 0 {
		t.Fatalf("an unchanged save must not request a scan, revision %d", rev)
	}
	if out.ReevaluatedReleases != nil {
		t.Fatalf("an unchanged save schedules no re-evaluation: %d", *out.ReevaluatedReleases)
	}

	// Title only.
	in = current()
	in.ExpectedVersion = version
	title := "Fixed Title"
	in.TitleOverride = &title
	out, err = service.PatchAnimeMetadataApplication(ctx, animeID, in)
	if err != nil {
		t.Fatalf("title patch: %v", err)
	}
	version = out.Version
	if rev := configScanRevision(t, db, animeID); rev != 1 {
		t.Fatalf("a title change requests one scan, revision %d", rev)
	}
	if out.ReevaluatedReleases != nil {
		t.Fatalf("a title-only save must not re-evaluate releases: %d", *out.ReevaluatedReleases)
	}

	// Season: a second write coalesces into the same row and re-evaluates.
	in = current()
	in.ExpectedVersion = version
	season := 2
	in.Season = &season
	out, err = service.PatchAnimeMetadataApplication(ctx, animeID, in)
	if err != nil {
		t.Fatalf("season patch: %v", err)
	}
	version = out.Version
	if out.ReevaluatedReleases == nil {
		t.Fatal("a season change re-evaluates the unsubmitted releases in the same call")
	}
	if rev := configScanRevision(t, db, animeID); rev != 2 {
		t.Fatalf("two writes coalesce into one row at revision 2, got %d", rev)
	}
	if n := configScanRows(t, db); n != 1 {
		t.Fatalf("one pending request per anime, got %d rows", n)
	}

	// The planned total never reaches a path: it re-evaluates but requests no scan.
	in = current()
	in.ExpectedVersion = version
	total := 24
	in.TotalEpisodes = &total
	out, err = service.PatchAnimeMetadataApplication(ctx, animeID, in)
	if err != nil {
		t.Fatalf("total patch: %v", err)
	}
	if out.ReevaluatedReleases == nil {
		t.Fatal("a total change re-evaluates releases")
	}
	if rev := configScanRevision(t, db, animeID); rev != 2 {
		t.Fatalf("a total-only change must not bump the request, revision %d", rev)
	}
}

// A naming-template change requests a scan; saving the same rules, or changing
// a section that never reaches a path, requests nothing.
func TestRulesPatchRequestsConfigScanOnlyForNaming(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "config-scan-rules-0001")
	animeID := subscribed.ManagedAnime.Id

	same, err := service.PatchAnimeRules(ctx, animeID, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: 1, Sections: ownershipDefaultRules(t).Sections,
	}))
	if err != nil {
		t.Fatalf("same rules: %v", err)
	}
	if rev := configScanRevision(t, db, animeID); rev != 0 {
		t.Fatalf("the same rules request no scan, revision %d", rev)
	}

	parser, err := service.PatchAnimeRules(ctx, animeID, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: generated.Version(same.Rules.Version), Sections: ownershipReplaceParserRules(t, []string{`\[(\d{1,4})\]`}).Sections,
	}))
	if err != nil {
		t.Fatalf("parser rules: %v", err)
	}
	if rev := configScanRevision(t, db, animeID); rev != 0 {
		t.Fatalf("a parser change reaches no path, revision %d", rev)
	}

	draft := ownershipReplaceParserRules(t, []string{`\[(\d{1,4})\]`})
	replace, err := draft.Sections.Naming.AsNamingRulesReplace()
	if err != nil {
		t.Fatal(err)
	}
	replace.Value.Tv.DirectoryTemplate += "-renamed"
	if err := draft.Sections.Naming.FromNamingRulesReplace(replace); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PatchAnimeRules(ctx, animeID, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: generated.Version(parser.Rules.Version), Sections: draft.Sections,
	})); err != nil {
		t.Fatalf("naming rules: %v", err)
	}
	if rev := configScanRevision(t, db, animeID); rev != 1 {
		t.Fatalf("a naming change requests one scan, revision %d", rev)
	}
}
