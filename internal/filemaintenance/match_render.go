package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"bansoko/internal/namingvars"
	namingvarspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

// File manager match: the target a detached video would take as one
// chosen season and episode of one anime. It is the ingest render over the
// anime's current TV naming template, with three differences that follow from
// the file having no frozen acquisition of its own:
//
//   - the episode is the user's choice, final as chosen: no offset is applied
//     and ep.source reads "manual";
//   - the anime facts are read now, from the persisted anime rows and its
//     current metadata binding, with the chosen season;
//   - the release-level facts (the Vars output and the rt / anibt value
//     namespaces, the release title, the source group and the probed media
//     facts) come from the file's own lineage when it has one — the placement
//     it came from, or, for a file that was never placed, the completed
//     operation that published it or its artifact file record at the path —
//     frozen then, and are absent otherwise, so a template that needs them
//     reports them missing instead of guessing.
//
// Nothing here writes. The provider episode titles are looked up again
// (bypassing the cache, like a user align) only when the template reads them.

// Match render failure codes (MatchRenderError.Code).
const (
	// MatchRenderFactsMissing: the template reads facts the evidence lacks.
	MatchRenderFactsMissing = "facts_missing"
	// MatchRenderTemplateInvalid: the template does not compile or render.
	MatchRenderTemplateInvalid = "template_invalid"
	// MatchRenderPathInvalid: the render produced no usable path.
	MatchRenderPathInvalid = "path_invalid"
	// MatchRenderEpisodeTitleLookup: the provider episode title lookup failed
	// transiently (no connection, network down, provider outage). The match
	// reports it instead of rendering a stale or empty title.
	MatchRenderEpisodeTitleLookup = "episode_title_lookup_failed"
)

// MatchRenderError is a render that cannot produce a target. It is a typed
// diagnostic for the preview, never a system fault.
type MatchRenderError struct {
	Code string
	// Facts are the catalog paths a MatchRenderFactsMissing render lacks.
	Facts []string
	cause error
}

func (e *MatchRenderError) Error() string {
	if e.cause != nil {
		return "match render " + e.Code + ": " + e.cause.Error()
	}
	return "match render " + e.Code
}

func (e *MatchRenderError) Unwrap() error { return e.cause }

// MatchRenderInput names what to render: the anime, the file, the episode, and
// the lineage that supplies release-level facts. A placement lineage is
// authoritative when the file has one; otherwise LineagePath (the file's
// absolute path) is read for the file's own artifact lineage.
type MatchRenderInput struct {
	ManagedAnimeID     string
	LineagePlacementID string
	// LineagePath is the file's absolute path; it is read only when
	// LineagePlacementID is empty.
	LineagePath string
	// FileName is the original base name of the file, extension included.
	FileName  string
	SizeBytes int64
	Season    int
	Episode   int
}

// MatchRender is a rendered match target.
type MatchRender struct {
	// Path is the rendered target: absolute — the directory template carries the
	// whole path, there is no implicit library root to prepend — and slash
	// separated.
	Path string
	// Lineage reports that release-level facts came from the file's lineage.
	Lineage bool
}

// matchLineage is the optional release-level evidence of a file.
type matchLineage struct {
	found          bool
	releaseID      string
	releaseVersion int
	releaseTitle   string
	sourceKind     string
	groupName      string
	sourceProvider string
	sourceGroupRef string
	sourceFacts    []byte
	fileFacts      []byte
	releaseFacts   []byte
	frozenConfig   []byte
}

// matchLineageOf reads the lineage of a placement leniently: any state of the
// placement, and every piece may be missing.
func (r *NamingTargetRenderer) matchLineageOf(ctx context.Context, placementID string) (matchLineage, error) {
	var l matchLineage
	if strings.TrimSpace(placementID) == "" {
		return l, nil
	}
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(rel.id,''), COALESCE(rel.version,0), COALESCE(rel.raw_facts->>'title',''), COALESCE(src.source_kind,''), COALESCE(src.group_name,''), COALESCE(src.provider,''), COALESCE(src.provider_group_ref,''),
 COALESCE(rel.source_facts,'{}'::jsonb), COALESCE(af.facts,'{}'::jsonb), COALESCE(snap.release_facts,'{}'::jsonb), COALESCE(fo.frozen_config_snapshot,'{}'::jsonb)
FROM placements p
JOIN file_operations fo ON fo.id=p.source_owner_ref AND p.source_evidence_type='file_operation'
LEFT JOIN artifact_files af ON af.id=fo.artifact_file_id
LEFT JOIN artifacts art ON art.id=fo.artifact_id
LEFT JOIN download_tasks dt ON dt.id=art.download_task_id
LEFT JOIN releases rel ON rel.id=dt.release_id
LEFT JOIN rss_sources src ON src.id=rel.rss_source_id
LEFT JOIN LATERAL (SELECT release_facts FROM media_fact_snapshots WHERE release_id=rel.id ORDER BY created_at DESC,id DESC LIMIT 1) snap ON true
WHERE p.id=$1`, placementID).Scan(&l.releaseID, &l.releaseVersion, &l.releaseTitle, &l.sourceKind, &l.groupName, &l.sourceProvider, &l.sourceGroupRef,
		&l.sourceFacts, &l.fileFacts, &l.releaseFacts, &l.frozenConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return matchLineage{}, nil
	}
	if err != nil {
		return matchLineage{}, fmt.Errorf("read match lineage: %w", err)
	}
	l.found = true
	return l, nil
}

// matchLineageByPath reads the file's own artifact lineage at one exact
// absolute path. A completed file operation that published the file to that
// path is the newest candidate, joined with the artifact file it published; an
// artifact file recorded at that source path is the fallback, for a file the
// acquisition left in its download directory. The newest candidate wins. The
// comparison is exact: an unmatched path has no lineage, which is the correct
// fail-closed answer.
func (r *NamingTargetRenderer) matchLineageByPath(ctx context.Context, absolutePath string) (matchLineage, error) {
	var l matchLineage
	absolutePath = strings.TrimSpace(absolutePath)
	if absolutePath == "" {
		return l, nil
	}
	err := r.db.QueryRowContext(ctx, `WITH candidates AS (
 SELECT fo.artifact_file_id AS artifact_file_id, COALESCE(fo.frozen_config_snapshot,'{}'::jsonb) AS frozen_config, fo.created_at, fo.id AS candidate_id
 FROM file_operations fo
 WHERE fo.state = 'completed' AND fo.target_path = $1
 UNION ALL
 SELECT src.id, '{}'::jsonb, src.created_at, src.id
 FROM artifact_files src
 WHERE src.source_path = $1
)
SELECT COALESCE(rel.id,''), COALESCE(rel.version,0), COALESCE(rel.raw_facts->>'title',''), COALESCE(src.source_kind,''), COALESCE(src.group_name,''), COALESCE(src.provider,''), COALESCE(src.provider_group_ref,''),
 COALESCE(rel.source_facts,'{}'::jsonb), COALESCE(af.facts,'{}'::jsonb), COALESCE(snap.release_facts,'{}'::jsonb), c.frozen_config
FROM candidates c
JOIN artifact_files af ON af.id=c.artifact_file_id
LEFT JOIN artifacts art ON art.id=af.artifact_id
LEFT JOIN download_tasks dt ON dt.id=art.download_task_id
LEFT JOIN releases rel ON rel.id=dt.release_id
LEFT JOIN rss_sources src ON src.id=rel.rss_source_id
LEFT JOIN LATERAL (SELECT release_facts FROM media_fact_snapshots WHERE release_id=rel.id ORDER BY created_at DESC,id DESC LIMIT 1) snap ON true
ORDER BY c.created_at DESC, c.candidate_id DESC
LIMIT 1`, absolutePath).Scan(&l.releaseID, &l.releaseVersion, &l.releaseTitle, &l.sourceKind, &l.groupName, &l.sourceProvider, &l.sourceGroupRef,
		&l.sourceFacts, &l.fileFacts, &l.releaseFacts, &l.frozenConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return matchLineage{}, nil
	}
	if err != nil {
		return matchLineage{}, fmt.Errorf("read match lineage by path: %w", err)
	}
	l.found = true
	return l, nil
}

// frozenVarsOutput is the Vars output the file's acquisition froze, when it
// evaluated ok; otherwise none.
func (l matchLineage) frozenVarsOutput() map[string]map[string]string {
	if !l.found {
		return nil
	}
	var config struct {
		Vars *namingvars.FrozenEvaluation `json:"vars"`
	}
	if json.Unmarshal(l.frozenConfig, &config) != nil || config.Vars == nil || config.Vars.Status != "ok" {
		return nil
	}
	return config.Vars.Output
}

// RenderMatch renders the target of a detached video as one episode of one
// anime. It reads persisted rows and, when the template reads a provider
// episode title, looks the title up again; it writes nothing.
func (r *NamingTargetRenderer) RenderMatch(ctx context.Context, in MatchRenderInput) (MatchRender, error) {
	if r == nil || r.db == nil || r.rulesSource == nil {
		return MatchRender{}, errors.New("naming target renderer is not wired")
	}
	animeID, fileName := strings.TrimSpace(in.ManagedAnimeID), strings.TrimSpace(in.FileName)
	if animeID == "" || fileName == "" || in.Season < 0 || in.Episode < 1 {
		return MatchRender{}, errors.New("match render requires an anime, a file name, a season and an episode")
	}
	anime, err := r.animeFacts(ctx, animeID)
	if err != nil {
		return MatchRender{}, err
	}
	evidence := namingvars.AnimeEvidence{AnimeID: animeID}
	var bgm, tmdb, kind sql.NullString
	var year sql.NullInt64
	err = r.db.QueryRowContext(ctx, `SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, year FROM anime_metadata WHERE managed_anime_id=$1`, animeID).Scan(&bgm, &tmdb, &kind, &year)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return MatchRender{}, fmt.Errorf("read match metadata binding: %w", err)
	}
	if id := strings.TrimSpace(bgm.String); id != "" {
		evidence.BgmID = id
	}
	// A match is a TV match: a TMDB work bound as a movie names no season of a
	// show, so its id and titles stay out of the render.
	if id := strings.TrimSpace(tmdb.String); id != "" && kind.String == "tv" {
		evidence.TmdbID, evidence.TmdbKind = id, "tv"
	}
	if err := namingvarspersistence.FillWorkTitles(ctx, r.db, &evidence); err != nil {
		return MatchRender{}, err
	}
	// A placement is authoritative when the file has one: it names the
	// acquisition that produced the file. A file with no placement falls back
	// to its own artifact lineage at its path, so a file the skip route left in
	// the download folder still renders the facts its acquisition froze.
	var lineage matchLineage
	if strings.TrimSpace(in.LineagePlacementID) != "" {
		lineage, err = r.matchLineageOf(ctx, in.LineagePlacementID)
	} else {
		lineage, err = r.matchLineageByPath(ctx, in.LineagePath)
	}
	if err != nil {
		return MatchRender{}, err
	}
	rules, err := r.rulesSource.EffectiveNamingRules(ctx, animeID)
	if err != nil {
		return MatchRender{}, err
	}

	number := strconv.Itoa(in.Episode)
	season := in.Season
	base := path.Base(strings.ReplaceAll(fileName, "\\", "/"))
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), ".")
	facts := naming.NamingContext{
		Anime: naming.AnimeNamingFacts{
			ID: animeID, Title: anime.title, TitleOverride: anime.titleOverride, ProviderTitle: anime.providerTitle,
			Year: ownerYear(year),
			// The season chosen for this file is the season it is named
			// with (it is also the TMDB season its episode is looked up in).
			Season: &season,
			BGM:    naming.BGMNamingFacts{WorkID: evidence.BgmID, Title: evidence.BgmTitle, TitleCN: evidence.BgmTitleCN, Year: providerYear(evidence.BgmYear)},
			TMDB: naming.TMDBNamingFacts{
				WorkID: evidence.TmdbID, MediaKind: "tv", Season: in.Season, SeasonPresent: true,
				TitleCN: evidence.TmdbTitleCN, OriginalTitle: evidence.TmdbOriginalTitle, Year: providerYear(evidence.TmdbYear),
			},
		},
		Source: naming.SourceNamingFacts{Kind: lineage.sourceKind, GroupName: strings.TrimSpace(lineage.groupName), Group: providerdomain.GroupRefOrZero(providerdomain.Kind(lineage.sourceProvider), lineage.sourceGroupRef)},
		Release: naming.ReleaseNamingFacts{
			ID: lineage.releaseID, Version: lineage.releaseVersion, OriginalTitle: strings.TrimSpace(lineage.releaseTitle),
			FrozenEpisode: &naming.FrozenEpisodeNamingFacts{Raw: number, Number: number, Display: number, Source: matchEpisodeSource},
		},
		File: naming.FileNamingFacts{
			OriginalName: base, Basename: strings.TrimSuffix(base, filepath.Ext(base)), Extension: extension,
			SizeBytes: in.SizeBytes, Kind: "video",
		},
	}
	if lineage.found {
		facts.File.Media.Resolution = mediaFactStringFromJSON(lineage.fileFacts, "resolution")
		facts.File.Media.VideoCodec = mediaFactStringFromJSON(lineage.fileFacts, "video_codec")
		facts.File.Media.AudioCodec = mediaFactStringFromJSON(lineage.fileFacts, "audio_codec")
		facts.File.Media.Container = mediaFactStringFromJSON(lineage.fileFacts, "container")
		facts.File.Media.HDR = mediaFactStringFromJSON(lineage.fileFacts, "hdr")
		if facts.File.Media.Resolution == "" {
			facts.File.Media.Resolution = mediaFactStringFromJSON(lineage.releaseFacts, "resolution")
		}
		namespaces, err := templatefacts.FromSourceFacts(lineage.sourceFacts)
		if err != nil {
			return MatchRender{}, &MatchRenderError{Code: MatchRenderFactsMissing, cause: renderFactsUnavailable("frozen source facts are invalid")}
		}
		facts = naming.WithTemplateFacts(facts, namespaces)
	}
	// Without a usable frozen Vars output the render has an empty Vars object
	// (never null): a template reading vars.* then reports it missing.
	vars := lineage.frozenVarsOutput()
	if vars == nil {
		vars = map[string]map[string]string{}
	}
	facts, err = naming.WithVars(facts, vars)
	if err != nil {
		return MatchRender{}, &MatchRenderError{Code: MatchRenderTemplateInvalid, cause: err}
	}
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: facts})
	// The chosen episode is a regular one whatever the release title says.
	resolved.Context.Parsed.Episode.SpecialKind = ""

	// The match render is on the episode-title whitelist: a template that
	// reads a provider episode title looks it up again before rendering. A
	// transient lookup failure is this match's typed diagnostic; a definite
	// absence renders the empty fact.
	titleTemplates, err := r.compileTitleTemplates(rules, resolved.Context, naming.RendererVersion)
	if err != nil {
		return MatchRender{}, classifyMatchRender(err)
	}
	if err := r.resolveEpisodeTitlesInto(ctx, titleTemplates, &resolved.Context); err != nil {
		return MatchRender{}, &MatchRenderError{Code: MatchRenderEpisodeTitleLookup, cause: err}
	}

	// A match is a new render: it uses the current renderer.
	target, err := renderResolvedTarget(base, rules, resolved.Context, nil, naming.RendererVersion)
	if err != nil {
		return MatchRender{}, classifyMatchRender(err)
	}
	target = filepath.ToSlash(target)
	if strings.TrimSpace(target) == "" {
		return MatchRender{}, &MatchRenderError{Code: MatchRenderPathInvalid}
	}
	return MatchRender{Path: target, Lineage: lineage.found}, nil
}

// matchEpisodeSource is the ep.source of a chosen episode.
const matchEpisodeSource = "manual"

// classifyMatchRender maps a render failure to its typed diagnostic.
func classifyMatchRender(err error) error {
	var missing *missingFactsError
	if errors.As(err, &missing) {
		return &MatchRenderError{Code: MatchRenderFactsMissing, Facts: missing.facts, cause: err}
	}
	var named *naming.Error
	if errors.As(err, &named) && naming.IsFactError(named.Code) {
		return &MatchRenderError{Code: MatchRenderFactsMissing, Facts: named.Missing, cause: err}
	}
	if errors.As(err, &named) && named.Code == naming.ErrCodeNamingVarsUnknown {
		// The template reads a Vars value this file's evidence does not carry
		// (a file with no acquisition has no Vars output).
		return &MatchRenderError{Code: MatchRenderFactsMissing, cause: err}
	}
	if errors.Is(err, naming.ErrRenderFactsUnavailable) {
		return &MatchRenderError{Code: MatchRenderFactsMissing, cause: err}
	}
	return &MatchRenderError{Code: MatchRenderTemplateInvalid, cause: err}
}

// ownerYear is the owner-edited anime year column (nil when unset).
func ownerYear(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	year := int(value.Int64)
	return &year
}
