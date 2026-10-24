package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/attrrules"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
)

// A finite one-shot command must resolve the same frozen media binding its
// revision froze. A movie draft has to materialize the canonical movie:0
// identity before the target/naming plan runs (otherwise the plan would render
// a TV episode for a movie), and a TV draft has to keep its bound season and
// apply its offset exactly once.

// movieOneShotFixture wires the shared target boundary for one movie draft and
// returns the service, the canonical movie target path, its expected size and
// the admission input. The learned target is produced by the same shared
// planner the admission runs, with the revision's own evidence.
func movieOneShotFixture(t *testing.T, seedTitle string, tmdb *animedomain.TMDBBinding, ownerSeason ...int) (*oneShotTestServices, string, int64, AcquireSeedInput) {
	t.Helper()
	db := animetest.OpenTestDB(t)
	payload := parseOneShotTestTorrent(t)
	service := oneShotNewTestService(t, db, &oneShotFakeTorrentFetcher{payload: payload}, nil)
	service.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{}, acquisitiontarget.NewStore(targetpersistence.NewRepository(db)))
	// The rendered movie target, and therefore the adoption write below, lands
	// under a root this test owns.
	_, pinnedRules := pinOneShotNamingRoot(t, db)
	// The download path needs one configured qB connection; the fixture never
	// talks to a real qB (the admission only anchors the connection id).
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('qb-movie-test', 'qbittorrent', 'movie-test', 'http://127.0.0.1:1', 'secret-ref', 'configured',
        '{"username":"tester","category":"","save_root":"/downloads","downloader_root":"/downloads","user_tags":[]}'::jsonb)`); err != nil {
		t.Fatalf("seed qb connection: %v", err)
	}

	draft := agentDraftWithLabel(t)
	draft.Title = "Example Movie"
	draft.Metadata = animedomain.AnimeMetadataDraft{
		Bgm:  &animedomain.BGMBinding{WorkId: "486109"},
		Tmdb: tmdb,
	}
	// The shipped default directory templates name ${anime.year} (a provider
	// observation) and the TV ones ${anime.season_folder} (the bound season), so the
	// fixture states the year the provider recorded for the works it binds.
	setOneShotOwnerYear(&draft)
	// The season folder names the owner season (draft metadata), never the
	// TMDB binding's season; a TV fixture states it as the match prefill would
	// unless the caller names a different owner season.
	if tmdb != nil && tmdb.MediaKind == animedomain.TMDBMediaKindTv {
		season := tmdb.Season
		if len(ownerSeason) > 0 {
			season = ownerSeason[0]
		}
		draft.Metadata.Season = &season
	}
	seed := oneShotAgentSeedForPayload(t, payload)
	recordOneShotSeedGroup(t, db, seed, oneShotSeedGroupName)
	seed.Title = seedTitle
	business, businessErr := frozenpolicy.SeedBusiness(context.Background(), db, `vars.test = {marker: input.release_title};`)
	if businessErr != nil {
		t.Fatal(businessErr)
	}
	// The draft carries the business snapshot, so its frozen naming section — not
	// the Global row — is what the saved revision and the admission render with.
	business = rootOneShotBusinessNaming(t, business, pinnedRules)
	draftCarriesBusinessSections(t, &draft, business)
	draft.Business = &business
	businessDigest, businessErr := business.Digest()
	if businessErr != nil {
		t.Fatal(businessErr)
	}
	draft.BusinessDigest = &businessDigest
	candidate, err := service.Draft.SaveAnimeConfigCandidate(context.Background(), draftapplication.CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	input := AcquireSeedInput{
		CandidateID: candidate.Id,
		Revision:    *candidate.CurrentRevision,
		Seed:        seed,
		Caller: AcquireSeedCaller{
			Kind:  AcquireSeedCallerAgent,
			Agent: &AgentSeedAuthorization{GrantedBy: "pg-test", AuthorizationRef: "one-shot-movie"},
		},
	}

	// The probe learns the target from the same revision evidence the admission
	// will resolve, so the assertion cannot agree with a wrong plan by accident.
	revision, err := service.Draft.GetAnimeConfigCandidateRevision(context.Background(), candidate.Id, *candidate.CurrentRevision)
	if err != nil {
		t.Fatalf("read revision: %v", err)
	}
	resolved, err := revision.Resolved()
	if err != nil {
		t.Fatalf("resolve revision sections: %v", err)
	}
	effective, err := revision.EffectiveConfig()
	if err != nil {
		t.Fatalf("effective config: %v", err)
	}
	selection, err := revision.ParserSelectionInput(resolved)
	if err != nil {
		t.Fatalf("revision parser selection: %v", err)
	}
	selection.Offset = 0 // the admission request carries no episode_offset
	item := buildOneShotFeedItem(payload, seed)
	evidence, err := acquisitiontarget.ResolveParserEvidence(context.Background(), selection, item.Title)
	if err != nil {
		t.Fatalf("resolve revision evidence: %v", err)
	}
	// Mirror the admission exactly: the same owner rows and the same evaluation
	// the one-shot builds from the resolved evidence (its FrozenMediaKind/
	// FrozenEpisode decide the naming branch), not a synthetic preview result.
	rules, _, err := attrrules.Current()
	if err != nil {
		t.Fatalf("active attr rules: %v", err)
	}
	hold := animedomain.MetadataHoldFromBindings(draft.Metadata.Bgm, draft.Metadata.Tmdb)
	evaluation := animedomain.EvaluateFeedItemWithIdentity(item,
		animedomain.SourceEvaluationInput{Index: 0, Kind: string(animedomain.SourceKindCustomRss), GroupName: oneShotSeedGroupName},
		effective, hold, rules, evidence.Identity)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	animeID := probeAnimeEvidenceTx(t, service, tx, draft)
	frozen, err := freezeBusinessVarsTx(context.Background(), tx, business, item, animeID, "", nil)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	evaluation.FrozenVars = &frozen
	plan := planOneShotTargets(context.Background(), acquisitiontarget.FilesystemProbe{}, oneShotTargetRequest(
		"release-movie-probe",
		draft,
		oneShotSeedGroupName,
		0,
		string(evidence.Selection.Kind()),
		effective,
		item,
		evaluation,
		&evidence,

		"skip",
	))
	if plan.Admission.Admission != acquisitiontarget.AdmissionDownload {
		t.Fatalf("probe admission = %+v, reason %q", plan.Admission, plan.PlanReason)
	}
	return service, plan.Set.Targets[0].TargetPath, plan.Set.Targets[0].ExpectedSizeBytes, input
}

func movieBinding() *animedomain.TMDBBinding {
	return &animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindMovie, Season: 0}
}

// A movie title with no episode, with a release year, and with a "- 07" token
// must all resolve the canonical movie:0 identity and render the movie naming
// branch — for the download path and for the adopted path alike.
func TestAcquireSeedOnceMovieUsesCanonicalMovieEvidenceAndMovieNaming(t *testing.T) {
	titles := []string{"Example Movie", "Example Movie (2024)", "Example Movie - 07"}
	for _, seedTitle := range titles {
		t.Run(seedTitle, func(t *testing.T) {
			service, targetPath, _, input := movieOneShotFixture(t, seedTitle, movieBinding())
			// The movie branch carries no season folder and names the file by the
			// file's own name — never the anime title and the SxxExx marker a TV
			// render of the same release would produce.
			if strings.Contains(targetPath, "Season") || strings.Contains(targetPath, "S01E") {
				t.Fatalf("movie target %q used the TV naming branch", targetPath)
			}
			if base := filepath.Base(targetPath); base != oneShotTestTorrentFileName {
				t.Fatalf("movie target basename = %q, want the file's own name %q", base, oneShotTestTorrentFileName)
			}
			if !strings.Contains(targetPath, "Example Movie") {
				t.Fatalf("movie target %q lost the anime title", targetPath)
			}

			result, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-movie-normal")
			if err != nil {
				t.Fatalf("movie admission: %v", err)
			}
			assertOneShotMovieRelease(t, service, result.ReleaseID, "accepted_pending_download")

			var storedPath string
			if err := service.db.QueryRowContext(context.Background(),
				`SELECT target_path FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID).Scan(&storedPath); err != nil {
				t.Fatal(err)
			}
			if storedPath != targetPath {
				t.Fatalf("frozen target = %q, want the movie plan %q", storedPath, targetPath)
			}
		})
	}
}

// The adopted path must use exactly the same movie evidence: the identity and
// the frozen target facts are the ones the probe learned, and no download work
// is fabricated.
func TestAcquireSeedOnceMovieAdoptedUsesTheSameMovieEvidence(t *testing.T) {
	service, targetPath, expectedSize, input := movieOneShotFixture(t, "Example Movie - 07", movieBinding())
	writeAdoptionTarget(t, targetPath, expectedSize)

	result, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-movie-adopted")
	if err != nil {
		t.Fatalf("adopted movie admission: %v", err)
	}
	if result.DownloadTaskID != nil {
		t.Fatalf("adopted movie admission fabricated download_task_id %q", *result.DownloadTaskID)
	}
	assertOneShotMovieRelease(t, service, result.ReleaseID, "adopted_existing")
	// The adoption is the same movie plan: the same target row, unchanged file.
	var storedPath string
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT target_path FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID).Scan(&storedPath); err != nil {
		t.Fatal(err)
	}
	if storedPath != targetPath {
		t.Fatalf("adopted frozen target = %q, want the movie plan %q", storedPath, targetPath)
	}
	contents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(contents)) != expectedSize {
		t.Fatalf("adopted target size = %d, want %d", len(contents), expectedSize)
	}
}

// assertOneShotMovieRelease pins the frozen Release evidence of a movie
// admission: the canonical movie:0 projection (episode scalar 0, never a year
// or a "- 07" number) and the movie media kind frozen into the input snapshot.
func assertOneShotMovieRelease(t *testing.T, service *oneShotTestServices, releaseID, wantVerdict string) {
	t.Helper()
	var episode sql.NullInt64
	var verdict string
	if err := service.Service.db.QueryRowContext(context.Background(), `
SELECT (parsed_facts->>'episode')::int, verdict FROM releases WHERE id = $1`, releaseID).
		Scan(&episode, &verdict); err != nil {
		t.Fatal(err)
	}
	if !episode.Valid || episode.Int64 != 0 {
		t.Fatalf("movie release episode = %+v, want the canonical 0", episode)
	}
	if verdict != wantVerdict {
		t.Fatalf("movie release verdict = %q, want %q", verdict, wantVerdict)
	}
	var frozenInput string
	if err := service.db.QueryRowContext(context.Background(), `
SELECT COALESCE(input_snapshot::text, '') FROM one_shot_acquisitions WHERE release_id = $1`, releaseID).Scan(&frozenInput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frozenInput, `"media_kind": "movie"`) {
		t.Fatalf("frozen one-shot input does not carry the movie media kind: %s", frozenInput)
	}
}

// TV keeps its bound season and applies the one-shot offset exactly once; a
// request without episode_offset resolves with 0. The movie switch must not
// regress either.
func TestAcquireSeedOnceTvKeepsSeasonAndAppliesOffsetOnce(t *testing.T) {
	service, targetPath, _, input := movieOneShotFixture(t, "Example Movie - 07",
		&animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindTv, Season: 2})
	// The TV branch renders the shipped TV layout: the kind word, the
	// "Title (Year)" name, the owner season folder and the episode marker.
	// The episode is the file's own ("Show 01 [1080p].mkv"), never the - 07
	// token of the release title: a per-file episode decides the render.
	if want := filepath.Join("TV", "Example Movie (2025)", "Season 02", "Example Movie S02E01.mkv"); !strings.HasSuffix(targetPath, want) {
		t.Fatalf("tv target %q is not the TV layout %q", targetPath, want)
	}
	result, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-tv-offset")
	if err != nil {
		t.Fatalf("tv admission: %v", err)
	}
	var episode sql.NullInt64
	if err := service.db.QueryRowContext(context.Background(), `
SELECT (parsed_facts->>'episode')::int FROM releases WHERE id = $1`, result.ReleaseID).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	if !episode.Valid || episode.Int64 != 7 {
		t.Fatalf("tv episode = %+v, want the raw 7 with the default zero request offset", episode)
	}
	var frozenInput string
	if err := service.db.QueryRowContext(context.Background(), `
SELECT COALESCE(input_snapshot::text, '') FROM one_shot_acquisitions WHERE release_id = $1`, result.ReleaseID).Scan(&frozenInput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frozenInput, `"media_kind": "movie"`) {
		t.Fatalf("tv admission froze a movie media kind: %s", frozenInput)
	}
}

// A one-shot names the owner season, not the TMDB binding's season: the draft
// says Season 3 while TMDB is bound to its season 1, and both the probe and the
// admitted target carry Season 03.
func TestAcquireSeedOnceNamesTheOwnerSeasonNotTheTMDBSeason(t *testing.T) {
	service, targetPath, _, input := movieOneShotFixture(t, "Example Movie - 07",
		&animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindTv, Season: 1}, 3)
	if want := filepath.Join("TV", "Example Movie (2025)", "Season 03", "Example Movie S03E01.mkv"); !strings.HasSuffix(targetPath, want) {
		t.Fatalf("tv target %q does not name the owner season %q", targetPath, want)
	}
	result, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-owner-season")
	if err != nil {
		t.Fatalf("tv admission: %v", err)
	}
	var storedPath string
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT target_path FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID).Scan(&storedPath); err != nil {
		t.Fatal(err)
	}
	if storedPath != targetPath {
		t.Fatalf("admitted target = %q, want %q", storedPath, targetPath)
	}
}

// The request episode_offset is the one-shot's only offset: it moves the
// resolved identity, lands on the placeholder link, freezes into the input
// snapshot, and is part of the replay identity. An out-of-range value is a
// typed 422 with zero writes.
func TestAcquireSeedOnceAppliesTheRequestEpisodeOffset(t *testing.T) {
	service, _, _, input := movieOneShotFixture(t, "Example Movie - 07",
		&animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindTv, Season: 2})
	ctx := context.Background()

	outOfRange := input
	outOfRange.EpisodeOffset = 1001
	if _, err := service.AcquireSeedOnce(ctx, outOfRange, "one-shot-offset-out-of-range"); !isBusinessCode(err, 422, "OFFSET_OUT_OF_RANGE") {
		t.Fatalf("out-of-range offset err = %v, want 422 OFFSET_OUT_OF_RANGE", err)
	}
	var markers int
	if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_acquisitions`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatalf("out-of-range offset wrote %d one-shot markers, want 0", markers)
	}

	input.EpisodeOffset = 2
	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-request-offset")
	if err != nil {
		t.Fatalf("offset admission: %v", err)
	}
	var episode sql.NullInt64
	var linkOffset int
	var frozenOffset sql.NullInt64
	if err := service.db.QueryRowContext(ctx, `
SELECT (r.parsed_facts->>'episode')::int, src.episode_offset, (o.input_snapshot->>'episode_offset')::int
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN one_shot_acquisitions o ON o.release_id = r.id
WHERE r.id = $1`, result.ReleaseID).Scan(&episode, &linkOffset, &frozenOffset); err != nil {
		t.Fatal(err)
	}
	if !episode.Valid || episode.Int64 != 9 {
		t.Fatalf("episode = %+v, want 7 shifted by the request offset 2", episode)
	}
	if linkOffset != 2 {
		t.Fatalf("placeholder link episode_offset = %d, want the request offset 2", linkOffset)
	}
	if !frozenOffset.Valid || frozenOffset.Int64 != 2 {
		t.Fatalf("frozen input episode_offset = %+v, want 2", frozenOffset)
	}

	replayed, err := service.AcquireSeedOnce(ctx, input, "one-shot-request-offset")
	if err != nil || replayed.OneShotID != result.OneShotID {
		t.Fatalf("same-offset replay = %+v, %v; want the original handle", replayed, err)
	}
	changed := input
	changed.EpisodeOffset = 3
	if _, err := service.AcquireSeedOnce(ctx, changed, "one-shot-request-offset"); !isBusinessCode(err, 409, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("same key with a different offset err = %v, want 409 IDEMPOTENCY_KEY_REUSED", err)
	}
	omitted := input
	omitted.EpisodeOffset = 0
	if _, err := service.AcquireSeedOnce(ctx, omitted, "one-shot-request-offset"); !isBusinessCode(err, 409, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("same key with the offset omitted err = %v, want 409 IDEMPOTENCY_KEY_REUSED", err)
	}

	// The first admission left a subscription whose link for this very seed
	// carries offset 2. A new one-shot of the same seed without episode_offset
	// must still resolve with 0: no existing link offset leaks into it.
	second, err := service.AcquireSeedOnce(ctx, omitted, "one-shot-request-offset-second")
	if err != nil {
		t.Fatalf("second admission: %v", err)
	}
	if err := service.db.QueryRowContext(ctx, `
SELECT (r.parsed_facts->>'episode')::int, src.episode_offset
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
WHERE r.id = $1`, second.ReleaseID).Scan(&episode, &linkOffset); err != nil {
		t.Fatal(err)
	}
	if !episode.Valid || episode.Int64 != 7 || linkOffset != 0 {
		t.Fatalf("second admission episode = %+v, link offset = %d; want 7 and 0, untouched by the existing link", episode, linkOffset)
	}
}

func isBusinessCode(err error, status int, code string) bool {
	var business *apperrors.Business
	return errors.As(err, &business) && business.Status == status && business.Code == code
}

// A metadata edit after submission must not move the frozen movie evidence: the
// Release projection, the frozen one-shot input and the target set stay exactly
// what the revision decided.
func TestAcquireSeedOnceMovieEvidenceDoesNotDriftAfterMetadataEdit(t *testing.T) {
	service, targetPath, _, input := movieOneShotFixture(t, "Example Movie (2024)", movieBinding())
	result, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-movie-drift")
	if err != nil {
		t.Fatalf("movie admission: %v", err)
	}
	before := frozenOneShotFacts(t, service, result.ReleaseID)

	// Rebind the instance metadata to TV after submission. The edit itself must
	// land (the one-shot mount is active, not retired), so the no-drift
	// assertion below is not vacuous.
	if _, err := service.db.ExecContext(context.Background(), `
UPDATE anime_metadata
SET tmdb_media_kind = 'tv', tmdb_season = 5, version = version + 1
WHERE managed_anime_id = $1`, result.ManagedAnimeID); err != nil {
		t.Fatalf("rebind metadata to tv: %v", err)
	}
	var reboundKind string
	if err := service.db.QueryRowContext(context.Background(), `
SELECT tmdb_media_kind FROM anime_metadata WHERE managed_anime_id = $1`, result.ManagedAnimeID).Scan(&reboundKind); err != nil {
		t.Fatal(err)
	}
	if reboundKind != "tv" {
		t.Fatalf("metadata rebind did not land: kind = %q", reboundKind)
	}
	after := frozenOneShotFacts(t, service, result.ReleaseID)
	if before != after {
		t.Fatalf("frozen movie evidence drifted after a metadata edit:\nbefore=%s\nafter=%s", before, after)
	}
	var storedPath string
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT target_path FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID).Scan(&storedPath); err != nil {
		t.Fatal(err)
	}
	if storedPath != targetPath {
		t.Fatalf("frozen target drifted to %q, want %q", storedPath, targetPath)
	}
}

func frozenOneShotFacts(t *testing.T, service *oneShotTestServices, releaseID string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(releaseID))
	var parsed, frozenInput string
	if err := service.db.QueryRowContext(context.Background(), `
SELECT COALESCE(r.parsed_facts::text, ''), COALESCE(o.input_snapshot::text, '')
FROM releases r
JOIN one_shot_acquisitions o ON o.release_id = r.id
WHERE r.id = $1`, releaseID).Scan(&parsed, &frozenInput); err != nil {
		t.Fatal(err)
	}
	return string(digest[:]) + parsed + frozenInput
}
