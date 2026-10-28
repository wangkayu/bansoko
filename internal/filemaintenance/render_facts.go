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

// placedRenderFacts is what a placed file contributes to a maintenance render:
// its frozen release and file facts (episode, rt.*, anibt.*, media.*, torrent.*,
// subgroup, release title, Vars). The anime's configuration (template, title,
// owner season and year, media kind, TMDB/BGM binding and bound-work titles) is
// never taken from here; it is read current by renderNamingContext.
type placedRenderFacts struct {
	placementID string
	lineage     bool
	// excluded marks a placement kept out of the instance's identity (see
	// ExcludedPlacementSQL): it never renders.
	excluded   bool
	releaseID  string
	sourceKind string
	groupName  string
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

// placedRenderFacts reads the frozen release and file evidence of one
// placement: the facts of the release and the file, never the configuration the
// file renders with (renderNamingContext reads that now). An excluded placement
// is reported as ErrPlacementExcluded before any lineage check.
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
	query := `SELECT p.id, ` + ExcludedPlacementSQL("p") + `, p.source_evidence_type = 'file_operation', COALESCE(rel.id,''), COALESCE(src.source_kind,''), COALESCE(src.group_name,''), COALESCE(src.provider,''), COALESCE(src.provider_group_ref,''), COALESCE(rel.raw_facts->>'title',''), COALESCE(rel.version,0), COALESCE(af.file_ref,''), COALESCE(mf.file_evidence->>'name', af.relative_path, ''), COALESCE(mf.size_bytes, af.size_bytes, 0), COALESCE(af.media_kind,''), COALESCE(af.facts,'{}'::jsonb), COALESCE(mf.effective_facts,'{}'::jsonb), COALESCE(snap.id,''), COALESCE(snap.release_facts,'{}'::jsonb), COALESCE(fo.frozen_config_snapshot,'{}'::jsonb), COALESCE(fo.frozen_render_snapshot,'{}'::jsonb), COALESCE(rel.source_facts,'{}'::jsonb), COALESCE(fo.id,'')
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
	err := r.db.QueryRowContext(ctx, query, animeID, placementID, path).Scan(&f.placementID, &f.excluded, &f.lineage, &f.releaseID, &f.sourceKind, &f.groupName, &f.sourceProvider, &f.sourceGroupRef, &f.originalTitle, &f.releaseVersion, &f.fileRef, &f.fileName, &f.fileSize, &f.fileKind, &f.fileFacts, &f.effectiveFacts, &f.snapshotID, &f.releaseFacts, &f.frozenConfig, &f.renderSnapshot, &f.sourceFacts, &f.fileOperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("read placed render lineage: %w", err)
	}
	if f.excluded {
		return f, true, ErrPlacementExcluded
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

// namingRenderer is the naming renderer version a render of the current
// configuration uses: the version the file effect recorded when it already
// names the owner season (naming-render-4 and later), naming-render-4
// otherwise (see naming.CurrentConfigRenderer).
func (f placedRenderFacts) namingRenderer() string {
	var snapshot struct {
		NamingRenderer string `json:"naming_renderer"`
	}
	if len(f.renderSnapshot) == 0 || json.Unmarshal(f.renderSnapshot, &snapshot) != nil {
		return naming.CurrentConfigRenderer("")
	}
	return naming.CurrentConfigRenderer(snapshot.NamingRenderer)
}

// renderFacts reads the replay facts the file effect recorded in the frozen
// render snapshot, keyed by the catalog paths naming.ReplayFactValues returns.
// An operation written before the field existed reports none, which leaves the
// required-fact check alone. The two metadata work ids are not replay facts of
// a maintenance render: they are the current binding, and a missing binding is
// the typed config-incomplete refusal instead.
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
	delete(snapshot.RenderFacts, "Anime.BGM.WorkID")
	delete(snapshot.RenderFacts, "Anime.TMDB.WorkID")
	return snapshot.RenderFacts
}

// frozenRenderBindingFacts is the metadata binding the file effect read when it
// rendered the target. A maintenance render reads only the torrent facts from
// it (they belong to the release); everything else is the current
// configuration (currentAnimeConfig).
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
	animeConfig, err := r.currentAnimeConfig(ctx, animeID)
	if err != nil {
		return naming.ResolvedContext{}, "", err
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
		Anime: naming.AnimeNamingFacts{ID: animeID, Title: anime.title, TitleOverride: anime.titleOverride, ProviderTitle: anime.providerTitle},
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
		Resolution string `json:"resolution"`
	}
	if err := json.Unmarshal(lineage.releaseFacts, &releaseFacts); err != nil {
		return naming.ResolvedContext{}, "", renderFactsUnavailable("frozen release facts are invalid")
	}
	if ctxFacts.File.Media.Resolution == "" {
		ctxFacts.File.Media.Resolution = strings.TrimSpace(releaseFacts.Resolution)
	}
	// The torrent facts belong to the release and stay as the effect read them.
	if binding, hasBinding := lineage.bindingFacts(); hasBinding {
		ctxFacts.Torrent = naming.TorrentNamingFacts{Size: binding.TorrentSize, FileCount: binding.TorrentFileCount, HashV1: binding.TorrentHashV1, HashV2: binding.TorrentHashV2}
	}
	// Everything the anime itself configures is read now: the media kind, the
	// owner season and year, and the metadata binding with its work titles. An
	// input the configuration lacks refuses the render; the frozen values are
	// never a fallback.
	animeConfig.apply(&ctxFacts.Anime)
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
