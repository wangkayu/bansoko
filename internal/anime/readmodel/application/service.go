package application

import (
	"context"
	"database/sql"
	"errors"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/release/facts"
)

type ConfigReader interface {
	ReadOverrides(context.Context, string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error)
}
type OwnerReader interface {
	ReadRulesOwnerApplication(context.Context, string) (ownershipapplication.RulesOwner, error)
	ReadSubgroupsOwnerApplication(context.Context, string, string) (ownershipapplication.SubgroupsOwnerResult, error)
	ReadRSSSourcesApplication(context.Context, string) ([]animeapplication.RSSSource, error)
	ReadRSSSourceRuntimesApplication(context.Context, string) ([]animeapplication.RSSSourceRuntime, error)
	PreviewSubgroupsSelectionApplication(context.Context, string, ownershipapplication.SubgroupsSection) ([]ownershipapplication.SubgroupSelectionPreviewFact, error)
}
type RevisionReader interface {
	ReadVerifiedAnimeConfigCandidateRevision(context.Context, string, int64) (animeapplication.CandidateRevision, error)
}
type SeedPreviewSource = animeapplication.SeedPreviewSource
type SeedTorrentManifestSource = animeapplication.SeedTorrentManifestSource

var ErrProviderFeedSnapshotRefreshRequired = animeapplication.ErrProviderFeedSnapshotRefreshRequired

type Dependencies struct {
	Config       ConfigReader
	Owners       OwnerReader
	Revisions    RevisionReader
	SeedPreviews SeedPreviewSource
	// SeedTorrentManifests is optional; nil renders every lazy seed as pending
	// because the read model then has no expansion cache to ask.
	SeedTorrentManifests SeedTorrentManifestSource
	MediaFacts           facts.MediaRepository
	// Observations is optional; nil reads provider group observations from the database.
	Observations GroupObservationReader
	// PlacementTargets is optional; nil shows an imported file's current
	// location without the pending new path.
	PlacementTargets PlacementTargetRenderer
}

type Service struct {
	db           *sql.DB
	config       ConfigReader
	owners       OwnerReader
	revisions    RevisionReader
	seedPreviews SeedPreviewSource
	// seedTorrentManifests reads the expansion cache for the seeds a snapshot
	// does not already carry a manifest for; nil means the cache is not wired.
	seedTorrentManifests SeedTorrentManifestSource
	mediaFacts           facts.MediaRepository
	// observations reads persisted provider group observations; nil reads the
	// database directly.
	observations GroupObservationReader
	// placementTargets renders the current-rules target of imported files.
	placementTargets PlacementTargetRenderer

	// previewMaterials caches the immutable attr rules and Vars programs a preview
	// evaluates with, so they are not re-read for every release.
	previewMaterials previewMaterialCache
	// loadAttrRulesSource and loadVarsProgramSource replace the database reads when
	// set; they exist so the loading behaviour is testable without a database.
	loadAttrRulesSource   func(context.Context, namingvars.ProgramRef) (attrrules.Rules, error)
	loadVarsProgramSource func(context.Context, namingvars.ProgramRef) (namingvars.Program, error)
}

func NewService(db *sql.DB, deps Dependencies) (*Service, error) {
	if db == nil {
		return nil, errors.New("anime readmodel database is nil")
	}
	if deps.Config == nil || deps.Owners == nil || deps.Revisions == nil {
		return nil, errors.New("anime readmodel collaborators are required")
	}
	return &Service{db: db, config: deps.Config, owners: deps.Owners, revisions: deps.Revisions, seedPreviews: deps.SeedPreviews, seedTorrentManifests: deps.SeedTorrentManifests, mediaFacts: deps.MediaFacts, observations: deps.Observations, placementTargets: deps.PlacementTargets}, nil
}

type ManagedAnime = animeapplication.ManagedAnime
type Subscription = animeapplication.Subscription
type AnimeMetadata = animeapplication.AnimeMetadata
type TitleSuggestion = animeapplication.TitleSuggestion
type MetadataHold = animeapplication.MetadataHold
type BGMBinding = animeapplication.BGMBinding
type TMDBBinding = animeapplication.TMDBBinding
type RulesOwner = ownershipapplication.RulesOwner
type SubgroupsOwnerResult = ownershipapplication.SubgroupsOwnerResult
type RSSSource = animeapplication.RSSSource
type RSSSourceRuntime = animeapplication.RSSSourceRuntime
type CandidateRevision = animeapplication.CandidateRevision
type SeedPreviewFact = animeapplication.SeedPreviewFact

// RulesPreviewInput is the root-owned unsaved owner rules patch; the
// owner_draft read mode carries it without introducing a second definition.
type RulesPreviewInput = animeapplication.RulesPreviewInput
type SeedPreviewSnapshot = animeapplication.SeedPreviewSnapshot
type FrozenDownloadInput = animeapplication.FrozenDownloadInput
type FrozenPolicySnapshot = animeapplication.FrozenPolicySnapshot

func (s *Service) readOverrides(ctx context.Context, id string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	return s.config.ReadOverrides(ctx, id)
}
func (s *Service) ReadVerifiedAnimeConfigCandidateRevision(ctx context.Context, id string, revision int64) (CandidateRevision, error) {
	return s.revisions.ReadVerifiedAnimeConfigCandidateRevision(ctx, id, revision)
}
func (s *Service) readRulesOwnerApplication(ctx context.Context, id string) (RulesOwner, error) {
	return s.owners.ReadRulesOwnerApplication(ctx, id)
}
func (s *Service) readSubgroupsOwnerApplication(ctx context.Context, id, subscriptionID string) (SubgroupsOwnerResult, error) {
	return s.owners.ReadSubgroupsOwnerApplication(ctx, id, subscriptionID)
}
func (s *Service) readRSSSourcesApplication(ctx context.Context, id string) ([]RSSSource, error) {
	return s.owners.ReadRSSSourcesApplication(ctx, id)
}
func (s *Service) readRSSSourceRuntimesApplication(ctx context.Context, id string) ([]RSSSourceRuntime, error) {
	return s.owners.ReadRSSSourceRuntimesApplication(ctx, id)
}

func applicationBindingsFromRow(bgm, tmdb, mediaKind sql.NullString, season sql.NullInt32) (*BGMBinding, *TMDBBinding) {
	var b *BGMBinding
	if bgm.Valid {
		b = &BGMBinding{WorkID: bgm.String}
	}
	var t *TMDBBinding
	if tmdb.Valid {
		t = &TMDBBinding{WorkID: tmdb.String, MediaKind: mediaKind.String}
		if season.Valid {
			t.Season = int(season.Int32)
		}
	}
	return b, t
}
func metadataHoldFromApplication(bgm *BGMBinding, tmdb *TMDBBinding) MetadataHold {
	var bDomain *animedomain.NullableBGMBinding
	if bgm != nil {
		bDomain = &animedomain.NullableBGMBinding{WorkId: bgm.WorkID}
	}
	var tDomain *animedomain.NullableTMDBBinding
	if tmdb != nil {
		tDomain = &animedomain.NullableTMDBBinding{WorkId: tmdb.WorkID, MediaKind: animedomain.TMDBMediaKind(tmdb.MediaKind), Season: tmdb.Season}
	}
	h := animedomain.MetadataHoldFromBindings(bDomain, tDomain)
	return MetadataHold{Held: h.Held, Missing: metadataHoldMissingStrings(h.Missing)}
}

func metadataHoldMissingStrings(values []animedomain.MetadataHoldMissing) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = string(value)
	}
	return result
}
func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	v := value.String
	return &v
}
func nullableIntPointer(value sql.NullInt32) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int32)
	return &v
}
func cloneNullableString(value sql.NullString) *string { return nullableStringPointer(value) }
func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}
func workspaceCollisionPolicy(value string) string {
	switch value {
	case "version":
		return "versioned"
	case "overwrite":
		return "overwrite"
	default:
		return "fail"
	}
}
func draftInvalidError(v []animedomain.FieldViolation) error {
	return animeapplication.DraftInvalidError(v)
}
func sectionResolutionError(err error) error { return animeapplication.SectionResolutionError(err) }
func canonicalAnimeDraftHash(d animedomain.AnimeDraft) (string, error) {
	return animeapplication.CanonicalAnimeDraftHash(d)
}
func canonicalJSONHash(v any) (string, error) { return animeapplication.CanonicalJSONHash(v) }
func safeReason(raw string) string            { return animeapplication.SafeReason(raw) }

// metadataPatchBindingsForDomain converts owner binding values through the
// shared kernel projection, so the read-model hold never depends on the
// ownership input shape.
func metadataPatchBindingsForDomain(bgm *BGMBinding, tmdb *TMDBBinding) (*animedomain.NullableBGMBinding, *animedomain.NullableTMDBBinding) {
	return animeapplication.NullableBindingsForDomain(bgm, tmdb)
}
