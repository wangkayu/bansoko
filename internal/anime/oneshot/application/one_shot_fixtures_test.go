package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	animetransport "bansoko/internal/anime/transport"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"

	"github.com/google/uuid"
)

// oneShotTestServices keeps the two owning services explicit in tests. The
// embedded service owns finite commands; Draft owns candidate draft operations.
type oneShotTestServices struct {
	*Service
	Draft *draftapplication.Service
}

func (services *oneShotTestServices) SaveAnimeConfigCandidate(ctx context.Context, input draftapplication.CandidateDraftInput) (draftapplication.AnimeConfigCandidate, error) {
	return services.Draft.SaveAnimeConfigCandidate(ctx, input)
}

func (services *oneShotTestServices) GetAnimeConfigCandidateRevision(ctx context.Context, candidateID string, revision int64) (animeapplication.CandidateRevision, error) {
	return services.Draft.GetAnimeConfigCandidateRevision(ctx, candidateID, revision)
}

func (services *oneShotTestServices) ActivateAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, idempotencyKey string, input draftapplication.CandidateSubscribeInput) (draftapplication.SubscribeResult, error) {
	return services.Draft.ActivateAnimeConfigCandidate(ctx, candidateID, expectedVersion, idempotencyKey, input)
}
func (services *oneShotTestServices) SetTorrentPayloadFetcher(fetcher animeapplication.TorrentPayloadFetcher) {
	if services != nil && services.Service != nil {
		services.Service.torrentFetcher = fetcher
	}
}

func (services *oneShotTestServices) SetTargetAcquisition(probe acquisitiontarget.Probe, store acquisitiontarget.Store) {
	if services != nil && services.Service != nil {
		services.Service.SetTargetAcquisition(probe, store)
	}
}

func oneShotNewTestService(t *testing.T, db *sql.DB, fetcher animeapplication.TorrentPayloadFetcher, previews animeapplication.SeedPreviewSource) *oneShotTestServices {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	markerRepo := animepersistence.NewRepository(db)
	parserPolicy := parserpolicy.NewService(db)
	draftService.SetFrozenParserPolicy(parserPolicy)
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	draftService.SetActivityWriter(activityWriter)
	provisioner := animeapplication.NewProvisioner(parserPolicy)
	deps := Dependencies{
		WorkItems:          store,
		OneShotMarkers:     markerRepo,
		Provisioner:        &provisioner,
		TorrentFetcher:     fetcher,
		SeedPreviews:       previews,
		CandidateRevisions: draftService,
	}
	deps.TargetProbe = acquisitiontarget.FilesystemProbe{}
	deps.TargetStore = acquisitiontarget.NewStore(targetpersistence.NewRepository(db))
	oneShotService, err := NewService(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	return &oneShotTestServices{Service: oneShotService, Draft: draftService}
}

// oneShotCandidateInput maps a wire draft and gives it the business snapshot a
// real draft carries from the moment it is created (the copy of Global).
func oneShotCandidateInput(t *testing.T, db *sql.DB, draft generated.AnimeDraft) draftapplication.CandidateDraftInput {
	t.Helper()
	mapped, err := animetransport.Draft(draft)
	if err != nil {
		t.Fatalf("map candidate draft: %v", err)
	}
	return draftapplication.CandidateDraftInput{Draft: presetdraft.LoadDomain(t, db, mapped)}
}

// candidateInput keeps migrated tests concise while candidate ownership stays root-side.
func candidateInput(t *testing.T, db *sql.DB, draft generated.AnimeDraft) draftapplication.CandidateDraftInput {
	return oneShotCandidateInput(t, db, draft)
}

// oneShotTestTorrentFileName is the one first-level file of the shared test
// payload (animetest.ValidTestTorrentBytes). A movie render names its target
// after exactly this name.
const oneShotTestTorrentFileName = "Show 01 [1080p].mkv"

// oneShotObservedYear is the owner-edited year a one-shot fixture sets. The
// shipped default directory templates name ${anime.year}, which reads only that
// value, so a fixture whose target must carry a year states it on the draft.
const oneShotObservedYear = 2025

// setOneShotOwnerYear sets the owner-edited year on a one-shot draft.
func setOneShotOwnerYear(draft *animedomain.AnimeDraft) {
	year := oneShotObservedYear
	draft.Metadata.Year = &year
}

// probeAnimeEvidenceTx writes the temporary owner rows an admission writes
// before it freezes Vars — the managed anime and the draft's metadata binding —
// and returns that anime id, so a probe freezes Vars with exactly the anime
// evidence the admission will read. The shipped default templates name
// ${anime.year} and ${anime.season_folder}, which only these rows carry, so a probe
// that passed no anime id would plan against blank evidence. The caller owns the
// transaction and rolls it back: a probe learns a target, it creates no owner.
func probeAnimeEvidenceTx(t *testing.T, service *oneShotTestServices, tx *sql.Tx, draft animedomain.AnimeDraft) string {
	t.Helper()
	animeID := uuid.NewString()
	if _, err := tx.ExecContext(context.Background(), `
INSERT INTO managed_anime(id, title, title_override) VALUES ($1, $2, $3)`,
		animeID, strings.TrimSpace(draft.Title), animeapplication.NullableOptionalText(draft.TitleOverride)); err != nil {
		t.Fatalf("probe owner row: %v", err)
	}
	if service == nil || service.Service == nil || service.Service.provisioner == nil {
		t.Fatal("probe needs the configured owner provisioner")
	}
	if _, err := service.Service.provisioner.InitializeAnimeOwnerRowsTx(context.Background(), tx, animeID, draft.Metadata); err != nil {
		t.Fatalf("probe metadata rows: %v", err)
	}
	return animeID
}

// pinOneShotNamingRoot writes the global naming templates under a scratch root
// and returns that root together with the rooted rules. Naming targets are
// absolute by construction — the render, not a global anchor, names the
// directory — so a fixture that materializes the rendered target must write its
// own directory into the section: the code default is the deployment mount
// /media, and a test must never write there.
func pinOneShotNamingRoot(t *testing.T, db *sql.DB) (string, animedomain.NamingRules) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := animedomain.DefaultSectionValue(animedomain.SectionNamingRules)
	if err != nil {
		t.Fatal(err)
	}
	rules, ok := value.(animedomain.NamingRules)
	if !ok {
		t.Fatalf("default naming section has type %T", value)
	}
	prefix := filepath.ToSlash(strings.TrimRight(root, "/"))
	rooted := func(template string) string { return strings.Replace(template, "/media", prefix, 1) }
	rules.Tv.DirectoryTemplate = rooted(rules.Tv.DirectoryTemplate)
	rules.Movie.DirectoryTemplate = rooted(rules.Movie.DirectoryTemplate)
	rules.Unknown.DirectoryTemplate = rooted(rules.Unknown.DirectoryTemplate)
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO global_config_sections (section_name, schema_version, value, version)
VALUES ($1, $2, $3, 1)
ON CONFLICT (section_name) DO UPDATE
SET value = EXCLUDED.value, version = global_config_sections.version + 1, updated_at = CURRENT_TIMESTAMP`,
		animedomain.SectionNamingRules, animedomain.SectionSchemaVersions[animedomain.SectionNamingRules], raw); err != nil {
		t.Fatalf("pin naming templates under %q: %v", root, err)
	}
	return root, rules
}

// rootOneShotBusinessNaming replaces the frozen naming section of a submitted
// business snapshot with the rooted rules. A draft that carries a business
// snapshot resolves every section from that snapshot and never reads the Global
// row, so pinning only the Global section would leave the fixture naming under
// the deployment mount /media. The snapshot is re-frozen so its digest still
// matches the submitted value.
func rootOneShotBusinessNaming(t *testing.T, business animedomain.BusinessSnapshot, rules animedomain.NamingRules) animedomain.BusinessSnapshot {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	business.Sections[animedomain.SectionNamingRules] = raw
	frozen, err := business.Freeze()
	if err != nil {
		t.Fatalf("freeze business naming under the pinned root: %v", err)
	}
	return frozen
}

// draftCarriesBusinessSections sets the draft's five rules sections to the
// values business freezes: a draft shows exactly the copy its snapshot holds.
func draftCarriesBusinessSections(t *testing.T, draft *animedomain.AnimeDraft, business animedomain.BusinessSnapshot) {
	t.Helper()
	for _, rule := range []struct {
		field   *json.RawMessage
		section string
	}{
		{&draft.Rules.Sections.Parser, animedomain.SectionParser},
		{&draft.Rules.Sections.ReleaseFilter, animedomain.SectionReleaseRules},
		{&draft.Rules.Sections.FileFilter, animedomain.SectionFileRules},
		{&draft.Rules.Sections.Naming, animedomain.SectionNamingRules},
		{&draft.Rules.Sections.FileEffect, animedomain.SectionFileEffect},
	} {
		wrapped, err := animedomain.SectionEnvelope(business.Sections[rule.section])
		if err != nil {
			t.Fatal(err)
		}
		*rule.field = wrapped
	}
}

func domainDraft(draft generated.AnimeDraft) (animedomain.AnimeDraft, error) {
	return animetransport.Draft(draft)
}

func humanCandidateForSeed(t *testing.T, services *oneShotTestServices, seed SelectedSeed) AcquireSeedInput {
	t.Helper()
	draft := oneShotProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, seed.ProviderEntryRef, seed.ProviderGroupRef)
	candidate, err := services.Draft.SaveAnimeConfigCandidate(context.Background(), oneShotCandidateInput(t, services.Service.db, draft))
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no revision")
	}
	return AcquireSeedInput{
		CandidateID: string(candidate.Id),
		Revision:    *candidate.CurrentRevision,
		Seed:        seed,
		Caller:      AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
}

func oneShotDefaultSubgroups(t *testing.T) generated.SubgroupsSection {
	t.Helper()
	return generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 3}
}

// oneShotDefaultRules returns rules carrying every section at its code
// default: the complete copy a draft created from a never-saved Global holds.
func oneShotDefaultRules(t *testing.T) generated.RulesDraft {
	t.Helper()
	return presetdraft.DefaultRules(t)
}

func oneShotTestDraft(t *testing.T) generated.AnimeDraft {
	t.Helper()
	season := 1
	return generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		InstanceLabel: animetest.StringPtr("test-instance"),
		Title:         "Test Show",
		Metadata: generated.AnimeMetadataDraft{
			Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-123"},
			Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-456", MediaKind: generated.TMDBMediaKindTv, Season: season},
		},
		Subgroups: oneShotDefaultSubgroups(t),
		Rules:     oneShotDefaultRules(t),
		RssSources: []generated.RSSSourceDraft{
			{SourceKind: generated.SourceKindCustomRss, Locator: "https://feeds.example.com/rss.xml", GroupName: "TestGroup", Title: "Test Show"},
		},
	}
}

func oneShotProviderDraftWithGroups(t *testing.T, origin generated.SubscriptionOrigin, entryRef string, groups ...string) generated.AnimeDraft {
	t.Helper()
	draft := oneShotTestDraft(t)
	draft.Origin = origin
	draft.OriginEntryRef = animetest.StringPtr(entryRef)
	draft.InstanceLabel = nil
	draft.Title = "Preview Show"
	draft.RssSources = []generated.RSSSourceDraft{}
	entries := make([]generated.SubgroupScoreEntry, 0, len(groups))
	for _, group := range groups {
		provider := generated.ProviderKindAnibt
		if origin == generated.SubscriptionOriginMikan {
			provider = generated.ProviderKindMikan
		}
		entries = append(entries, generated.SubgroupScoreEntry{
			Provider: provider, GroupRef: group, DisplayName: "Group " + group,
			SelectionMode: generated.SelectionModeAuto, Score: 50, CountInLimit: true,
			EffectiveSelected: false,
		})
	}
	draft.Subgroups = generated.SubgroupsSection{MaxRss: 3, Entries: entries}
	return draft
}
