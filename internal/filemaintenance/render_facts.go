package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

type placedRenderFacts struct {
	placementID string
	lineage     bool
	releaseID   string
	sourceKind  string
	groupName   string
	// sourceProvider/sourceGroupRef are the provider-namespaced external group
	// identity read from the same source row as groupName.
	sourceProvider string
	sourceGroupRef string
	originalTitle  string
	releaseVersion int
	fileRef        string
	fileName       string
	fileSize       int64
	fileKind       string
	fileFacts      []byte
	effectiveFacts []byte
	releaseFacts   []byte
	sourceFacts    []byte
	frozenConfig   []byte
	renderSnapshot []byte
	snapshotID     string
	// fileOperationID is the operation that placed the file.
	fileOperationID string
}

type frozenEffectiveFileFacts struct {
	Identity struct {
		State           string            `json:"State"`
		RawEpisode      *string           `json:"RawEpisode"`
		Episode         *string           `json:"Episode"`
		Offset          int               `json:"Offset"`
		SelectedSource  *string           `json:"SelectedSource"`
		CoverageKey     *string           `json:"CoverageKey"`
		EpisodeEnd      *string           `json:"EpisodeEnd"`
		Kind            string            `json:"Kind"`
		Shape           string            `json:"Shape"`
		ResolverVersion string            `json:"ResolverVersion"`
		InputDigest     string            `json:"InputDigest"`
		FailureCode     *string           `json:"FailureCode"`
		Alternatives    []json.RawMessage `json:"Alternatives"`
		Conflicts       []json.RawMessage `json:"Conflicts"`
	} `json:"identity"`
	Quality struct {
		Group      string            `json:"group"`
		Languages  []string          `json:"languages"`
		Resolution string            `json:"resolution"`
		States     map[string]string `json:"states"`
	} `json:"quality"`
	MappingVersion string `json:"mapping_version"`
}

func (f placedRenderFacts) effective() (frozenEffectiveFileFacts, error) {
	var decoded frozenEffectiveFileFacts
	if len(f.effectiveFacts) == 0 || string(f.effectiveFacts) == "{}" || string(f.effectiveFacts) == "null" {
		return decoded, renderFactsUnavailable("frozen file facts are missing")
	}
	if err := json.Unmarshal(f.effectiveFacts, &decoded); err != nil || decoded.Identity.State == "" {
		return decoded, renderFactsUnavailable("frozen file facts are invalid")
	}
	return decoded, nil
}

func (f placedRenderFacts) frozenEpisode() (*naming.FrozenEpisodeNamingFacts, error) {
	effective, err := f.effective()
	if err != nil {
		return nil, err
	}
	identity := effective.Identity
	if identity.State != "resolved" || identity.RawEpisode == nil || identity.Episode == nil || identity.SelectedSource == nil || *identity.SelectedSource == "" {
		return nil, renderFactsUnavailable("frozen episode identity is unresolved")
	}
	frozen := &naming.FrozenEpisodeNamingFacts{Raw: *identity.RawEpisode, Number: *identity.Episode, Offset: identity.Offset, Display: *identity.Episode, Source: string(*identity.SelectedSource)}
	if identity.EpisodeEnd != nil {
		frozen.NumberEnd = *identity.EpisodeEnd
	}
	return frozen, nil
}

// placedRenderFacts reads the frozen render evidence of one placement.
//
// The placement identity is the id when the caller has one. The path is only
// the identifier of the legacy callers that know no id, so a calibration row
// recorded before an alignment moved the file still renders: the target is a
// function of the placement's frozen lineage and the anime's current rules,
// never of where the file currently sits. Fencing on the path instead made
// every post-move read report no target, and the files read then showed a file
// the alignment had just placed at its target as unscanned.
func (r *NamingTargetRenderer) placedRenderFacts(ctx context.Context, animeID, path, placementID string) (placedRenderFacts, bool, error) {
	var f placedRenderFacts
	query := `SELECT p.id, p.source_evidence_type = 'file_operation', COALESCE(rel.id,''), COALESCE(src.source_kind,''), COALESCE(src.group_name,''), COALESCE(src.provider,''), COALESCE(src.provider_group_ref,''), COALESCE(rel.raw_facts->>'title',''), COALESCE(rel.version,0), COALESCE(af.file_ref,''), COALESCE(mf.file_evidence->>'name', af.relative_path, ''), COALESCE(mf.size_bytes, af.size_bytes, 0), COALESCE(af.media_kind,''), COALESCE(af.facts,'{}'::jsonb), COALESCE(mf.effective_facts,'{}'::jsonb), COALESCE(snap.id,''), COALESCE(snap.release_facts,'{}'::jsonb), COALESCE(fo.frozen_config_snapshot,'{}'::jsonb), COALESCE(fo.frozen_render_snapshot,'{}'::jsonb), COALESCE(rel.source_facts,'{}'::jsonb), COALESCE(fo.id,'')
FROM placements p
LEFT JOIN file_operations fo ON fo.id=p.source_owner_ref AND p.source_evidence_type='file_operation'
LEFT JOIN artifact_files af ON af.id=fo.artifact_file_id
LEFT JOIN artifacts art ON art.id=fo.artifact_id
LEFT JOIN download_tasks dt ON dt.id=art.download_task_id
LEFT JOIN releases rel ON rel.id=dt.release_id
LEFT JOIN rss_sources src ON src.id=rel.rss_source_id
LEFT JOIN LATERAL (SELECT id, release_facts FROM media_fact_snapshots WHERE release_id=rel.id ORDER BY created_at DESC,id DESC LIMIT 1) snap ON true
LEFT JOIN media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=af.file_ref
WHERE p.managed_anime_id=$1 AND p.state IN ('active','superseded')
  AND CASE WHEN NULLIF($2,'') IS NULL THEN p.physical_path=$3 ELSE p.id=$2 END
ORDER BY p.created_at DESC LIMIT 1`
	err := r.db.QueryRowContext(ctx, query, animeID, placementID, path).Scan(&f.placementID, &f.lineage, &f.releaseID, &f.sourceKind, &f.groupName, &f.sourceProvider, &f.sourceGroupRef, &f.originalTitle, &f.releaseVersion, &f.fileRef, &f.fileName, &f.fileSize, &f.fileKind, &f.fileFacts, &f.effectiveFacts, &f.snapshotID, &f.releaseFacts, &f.frozenConfig, &f.renderSnapshot, &f.sourceFacts, &f.fileOperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("read placed render lineage: %w", err)
	}
	if !f.lineage {
		return f, true, nil
	}
	if f.releaseID == "" || f.snapshotID == "" || f.fileRef == "" || f.fileName == "" {
		return f, true, renderFactsUnavailable("frozen snapshot file identity unavailable")
	}
	if _, err := f.effective(); err != nil {
		return f, true, err
	}
	if f.originalTitle == "" || f.sourceKind == "" || f.groupName == "" || string(f.releaseFacts) == "{}" {
		return f, true, renderFactsUnavailable("frozen release or source facts unavailable")
	}
	return f, true, nil
}

func renderFactsUnavailable(reason string) error {
	return fmt.Errorf("%w: %s", naming.ErrRenderFactsUnavailable, reason)
}

// providerYear narrows an optional provider year into the naming projection
// type. A value outside the supported provider range is not an air/release
// year and is treated as unknown, the same rule every writer applies.
func providerYear(value *int64) *int {
	if value == nil {
		return nil
	}
	year := int(*value)
	if year < templatefacts.MinYear || year > templatefacts.MaxYear {
		return nil
	}
	return &year
}

// namingRenderer is the naming renderer version the file effect recorded
// ("" for a snapshot written before the field existed, which replays
// naming-render-1).
func (f placedRenderFacts) namingRenderer() string {
	var snapshot struct {
		NamingRenderer string `json:"naming_renderer"`
	}
	if len(f.renderSnapshot) == 0 || json.Unmarshal(f.renderSnapshot, &snapshot) != nil {
		return ""
	}
	return snapshot.NamingRenderer
}

// renderFacts reads the replay facts the file effect recorded in the frozen
// render snapshot, keyed by the catalog paths naming.ReplayFactValues returns.
// An operation written before the field existed reports none, which leaves the
// required-fact check alone.
func (f placedRenderFacts) renderFacts() map[string]string {
	if len(f.renderSnapshot) == 0 {
		return nil
	}
	var snapshot struct {
		RenderFacts map[string]string `json:"render_facts"`
	}
	if json.Unmarshal(f.renderSnapshot, &snapshot) != nil {
		return nil
	}
	return snapshot.RenderFacts
}

// frozenRenderBindingFacts is the metadata binding the file effect read when it
// rendered the target: exactly the facts naming takes from the binding, which
// are the media-kind branch, the season folder and the two work ids.
type frozenRenderBindingFacts struct {
	MediaKind string `json:"media_kind"`
	Season    int    `json:"season"`
	// SeasonBound records that the render read Season as a bound season (a
	// season 0 included); absent in older snapshots.
	SeasonBound bool   `json:"season_bound"`
	TMDBWorkID  string `json:"tmdb_work_id"`
	BGMWorkID   string `json:"bgm_work_id"`
	// AnimeYear is the owner-edited year (anime.year) a naming-render-2
	// render read; absent in older snapshots.
	AnimeYear *int `json:"anime_year"`
	// AnimeSeason is the owner-edited season (anime.season) a naming-render-4
	// render read: JSON null when unset, absent in older snapshots (whose
	// renderers name the season of the TMDB binding instead).
	AnimeSeason json.RawMessage `json:"anime_season"`
	// The native work titles, provider years and torrent metadata the render
	// read; absent in snapshots recorded before they were frozen, so they
	// read as unknown there.
	BGMTitle          string `json:"bgm_title"`
	BGMTitleCN        string `json:"bgm_title_cn"`
	BGMYear           *int   `json:"bgm_year"`
	TMDBTitleCN       string `json:"tmdb_title_cn"`
	TMDBOriginalTitle string `json:"tmdb_original_title"`
	TMDBYear          *int   `json:"tmdb_year"`
	TorrentSize       *int64 `json:"torrent_size"`
	TorrentFileCount  *int64 `json:"torrent_file_count"`
	TorrentHashV1     string `json:"torrent_hash_v1"`
	TorrentHashV2     string `json:"torrent_hash_v2"`
}

// ownerSeason is the owner-edited season the render read. A snapshot that
// recorded none predates the owner season: it names the season its TMDB
// binding named (naming.AnimeSeason), the value its renderer replays anyway.
func (b frozenRenderBindingFacts) ownerSeason(tmdb naming.TMDBNamingFacts) *int {
	if len(b.AnimeSeason) == 0 {
		legacy := naming.AnimeSeason("", tmdb, false)
		if legacy == nil {
			return nil
		}
		season := int(*legacy)
		return &season
	}
	var season *int
	if json.Unmarshal(b.AnimeSeason, &season) != nil || season == nil || *season < 0 {
		return nil
	}
	return season
}

// bindingFacts reads the frozen render snapshot's recorded binding. It reports
// false for an operation written before the field existed, whose snapshot only
// records the rendered output; those rows keep the release-snapshot behaviour.
func (f placedRenderFacts) bindingFacts() (frozenRenderBindingFacts, bool) {
	raw := strings.TrimSpace(string(f.renderSnapshot))
	if raw == "" || raw == "{}" || raw == "null" {
		return frozenRenderBindingFacts{}, false
	}
	var snapshot struct {
		BindingFacts *frozenRenderBindingFacts `json:"binding_facts"`
	}
	if json.Unmarshal(f.renderSnapshot, &snapshot) != nil || snapshot.BindingFacts == nil {
		return frozenRenderBindingFacts{}, false
	}
	return *snapshot.BindingFacts, true
}

func (r *NamingTargetRenderer) renderNamingContext(ctx context.Context, animeID, path string, size int64, placementID string, frozen *naming.FrozenEpisodeNamingFacts) (naming.ResolvedContext, string, error) {
	anime, err := r.animeFacts(ctx, animeID)
	if err != nil {
		return naming.ResolvedContext{}, "", err
	}
	lineage, found, err := r.placedRenderFacts(ctx, animeID, path, placementID)
	if err != nil {
		return naming.ResolvedContext{}, "", err
	}
	// A placement that does not descend from a completed file operation has no
	// frozen acquisition evidence at all: the render is blocked instead of
	// re-deriving facts from the current configuration or the file name.
	if !found || !lineage.lineage {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("placement has no file-operation lineage")
	}
	base := filepath.Base(lineage.fileName)
	size = lineage.fileSize
	frozenEpisode := frozen
	if frozenEpisode == nil {
		frozenEpisode, err = lineage.frozenEpisode()
		if err != nil {
			return naming.ResolvedContext{}, "", err
		}
	}
	ctxFacts := naming.NamingContext{
		Anime: naming.AnimeNamingFacts{ID: animeID, Title: anime.title, TitleOverride: anime.titleOverride, ProviderTitle: anime.providerTitle, BGM: anime.bgm, TMDB: anime.tmdb},
		File: naming.FileNamingFacts{
			OriginalName: base,
			Basename:     strings.TrimSuffix(base, filepath.Ext(base)),
			Extension:    strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), "."),
			SizeBytes:    size, Kind: "video",
		},
		Release: naming.ReleaseNamingFacts{OriginalTitle: "", FrozenEpisode: frozenEpisode},
	}
	ctxFacts.Release.ID = lineage.releaseID
	ctxFacts.Release.Version = lineage.releaseVersion
	ctxFacts.Release.OriginalTitle = lineage.originalTitle
	ctxFacts.Source = naming.SourceNamingFacts{Kind: lineage.sourceKind, GroupName: lineage.groupName, Group: providerdomain.GroupRefOrZero(providerdomain.Kind(lineage.sourceProvider), lineage.sourceGroupRef)}
	ctxFacts.File.Kind = lineage.fileKind
	if ctxFacts.File.Kind == "" || ctxFacts.File.Kind == "video" {
		ctxFacts.File.Media.Resolution = mediaFactStringFromJSON(lineage.fileFacts, "resolution")
		ctxFacts.File.Media.VideoCodec = mediaFactStringFromJSON(lineage.fileFacts, "video_codec")
		ctxFacts.File.Media.AudioCodec = mediaFactStringFromJSON(lineage.fileFacts, "audio_codec")
		ctxFacts.File.Media.Container = mediaFactStringFromJSON(lineage.fileFacts, "container")
		ctxFacts.File.Media.HDR = mediaFactStringFromJSON(lineage.fileFacts, "hdr")
	} else {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("placement is not a video file")
	}
	var releaseFacts struct {
		Resolution       string `json:"resolution"`
		MediaKind        string `json:"media_kind"`
		Season           int    `json:"season"`
		SeasonBound      bool   `json:"season_bound"`
		BGMWorkID        string `json:"bgm_work_id"`
		TMDBWorkID       string `json:"tmdb_work_id"`
		ParserProvenance struct {
			MediaKind   string `json:"media_kind"`
			Season      int    `json:"season"`
			SeasonBound bool   `json:"season_bound"`
		} `json:"parser_provenance"`
	}
	if err := json.Unmarshal(lineage.releaseFacts, &releaseFacts); err != nil {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("frozen release facts are invalid")
	}
	if ctxFacts.File.Media.Resolution == "" {
		ctxFacts.File.Media.Resolution = strings.TrimSpace(releaseFacts.Resolution)
	}
	mediaKind, season := releaseFacts.MediaKind, releaseFacts.Season
	if releaseFacts.ParserProvenance.MediaKind != "" {
		mediaKind, season = releaseFacts.ParserProvenance.MediaKind, releaseFacts.ParserProvenance.Season
	}
	// The binding the file effect actually read wins over the release snapshot.
	// The snapshot is frozen when the release is evaluated, so a TMDB binding
	// set between that evaluation and the effect exists nowhere else, and
	// re-rendering without it disagrees with the placement the effect committed.
	// A recorded binding is a complete answer: it carries all four facts,
	// including the ones that were empty, so the season and the work ids are
	// taken from it verbatim — falling back per field would resurrect a value
	// the effect did not use. An operation written before the field existed has
	// no binding record and keeps the release-snapshot behaviour.
	// Never fall back to the current metadata row: a metadata edit after the
	// effect must not move the render.
	binding, hasBinding := lineage.bindingFacts()
	if hasBinding {
		season = binding.Season
		if binding.MediaKind != "" {
			mediaKind = binding.MediaKind
		}
	}
	if mediaKind != "tv" && mediaKind != "movie" {
		// A snapshot frozen before the binding existed still carries the typed
		// episode identity, which is the same evidence the offset planner reads
		// (frozenIdentityMediaKind). Never fall back to the current metadata row:
		// a metadata edit after acquisition must not move the render branch.
		effective, effectiveErr := lineage.effective()
		if effectiveErr != nil {
			return naming.ResolvedContext{}, "", effectiveErr
		}
		if derived := frozenIdentityMediaKind(effective.Identity.Kind); derived != "" {
			mediaKind = derived
		}
	}
	if mediaKind != "tv" && mediaKind != "movie" {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("frozen media kind is unavailable")
	}
	ctxFacts.Anime.TMDB.MediaKind, ctxFacts.Anime.TMDB.Season = mediaKind, season
	if hasBinding {
		// The recorded presence is what the render read: a bound season 0
		// renders Season 00, an unbound one reads no season.
		ctxFacts.Anime.TMDB.SeasonPresent = binding.SeasonBound
		ctxFacts.Anime.TMDB.SeasonBound = binding.SeasonBound && season == 0
		ctxFacts.Anime.BGM.WorkID = strings.TrimSpace(binding.BGMWorkID)
		ctxFacts.Anime.TMDB.WorkID = strings.TrimSpace(binding.TMDBWorkID)
		ctxFacts.Anime.BGM.Title, ctxFacts.Anime.BGM.TitleCN = binding.BGMTitle, binding.BGMTitleCN
		ctxFacts.Anime.BGM.Year = binding.BGMYear
		ctxFacts.Anime.Year = binding.AnimeYear
		ctxFacts.Anime.Season = binding.ownerSeason(ctxFacts.Anime.TMDB)
		ctxFacts.Anime.TMDB.TitleCN, ctxFacts.Anime.TMDB.OriginalTitle = binding.TMDBTitleCN, binding.TMDBOriginalTitle
		ctxFacts.Anime.TMDB.Year = binding.TMDBYear
		ctxFacts.Torrent = naming.TorrentNamingFacts{Size: binding.TorrentSize, FileCount: binding.TorrentFileCount, HashV1: binding.TorrentHashV1, HashV2: binding.TorrentHashV2}
	} else {
		ctxFacts.Anime.BGM.WorkID = strings.TrimSpace(releaseFacts.BGMWorkID)
		ctxFacts.Anime.TMDB.WorkID = strings.TrimSpace(releaseFacts.TMDBWorkID)
		ctxFacts.Anime.Season = frozenRenderBindingFacts{}.ownerSeason(ctxFacts.Anime.TMDB)
	}
	if frozen == nil {
		ctxFacts.Release.FrozenEpisode, err = lineage.frozenEpisode()
		if err != nil {
			return naming.ResolvedContext{}, "", err
		}
	} else {
		ctxFacts.Release.FrozenEpisode = frozen
	}
	// The value namespaces are the Release's frozen source facts, the same
	// block the file effect rendered with; they are never re-derived.
	namespaces, err := templatefacts.FromSourceFacts(lineage.sourceFacts)
	if err != nil {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("frozen source facts are invalid")
	}
	ctxFacts = naming.WithTemplateFacts(ctxFacts, namespaces)
	var config struct {
		Vars *namingvars.FrozenEvaluation `json:"vars"`
	}
	if err := json.Unmarshal(lineage.frozenConfig, &config); err != nil || config.Vars == nil || config.Vars.Status != "ok" {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("frozen Vars output unavailable")
	}
	ctxFacts, err = naming.WithVars(ctxFacts, config.Vars.Output)
	if err != nil {
		return naming.ResolvedContext{}, "", err
	}
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: ctxFacts})
	return resolved, base, err
}

func mediaFactStringFromJSON(raw []byte, key string) string {
	var facts map[string]any
	if json.Unmarshal(raw, &facts) != nil {
		return ""
	}
	value, ok := facts[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// missingTemplateFacts returns the sorted facts a maintenance re-render must
// not silently drop, in two classes:
//
//   - a fact the compiled template reads outside any conditional and the current
//     context cannot supply: the template writes it unconditionally, so an empty
//     value is a degraded name rather than the template's own handling of an
//     absent fact;
//   - a fact the file effect recorded as non-empty in the operation's frozen
//     render snapshot that the current context can no longer supply: the effect
//     rendered it, so dropping it would rename the file away from what
//     acquisition committed.
//
// The requirement set comes from the template expression, not from a textual
// scan: a fact the template only reads inside a conditional or through
// defaultValue has a defined empty value (the built-in filename template omits
// `[HEVC]` and the language tag exactly that way), so on its own its absence is
// the template's design. Both classes are bounded to the catalog paths in
// `naming.ReplayFactValues`, the same set the effect writes into the snapshot.
//
// One exemption: the provider episode title facts never entered a snapshot's
// replay set and no legacy render ever carried them — the facts postdate the
// legacy renderer. Every render they appear in draws either the stored title
// or the resolver's definitive answer (a provider absence, not_bound or
// no_episode), all of which render the empty fact, so the legacy refusal is
// never about them.
func missingTemplateFacts(engine *naming.Engine, required []string, recorded map[string]string, ctx naming.NamingContext) []string {
	current := naming.ReplayFactValues(ctx)
	missing := map[string]bool{}
	// Under naming-render-1 a required fact with no value refused the render
	// that placed the file, so the replay refuses it too (the engine would, but
	// this keeps the typed render-facts-unavailable answer). From
	// naming-render-2 on an empty fact rendered empty at placement as well.
	// The provider episode titles are the exception; the engine applies the
	// same one at render time.
	if engine.Legacy() {
		for _, path := range engine.MissingRequiredFacts(required, ctx) {
			if templatefacts.IsEpisodeTitleFactPath(path) {
				continue
			}
			missing[path] = true
		}
	}
	// The frozen-evidence guard: a fact the effect recorded that the current
	// evidence no longer has.
	for path, value := range recorded {
		if value == "" {
			continue
		}
		if currentValue, tracked := current[path]; tracked && currentValue == "" {
			missing[path] = true
		}
	}
	paths := make([]string, 0, len(missing))
	for path := range missing {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
