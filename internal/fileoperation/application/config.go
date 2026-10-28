package application

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/parser"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

// ErrFrozenConfigRequired is the typed fail-closed outcome when an admitted
// artifact's DownloadTask carries no complete frozen effective configuration. An
// automatic file effect must never fall back to the current global/instance
// configuration: doing so would let a config edit made after admission change
// the filter, naming, or file-effect decision of already admitted work.
var ErrFrozenConfigRequired = errors.New("admitted file operation requires the DownloadTask frozen effective configuration")

// effectiveConfigFromFrozenDownloadPolicy builds the file-operation config from
// the DownloadTask's frozen policy snapshot. The frozen payload carries the
// complete resolved section values, so this reads no current global or instance
// configuration. A missing or incomplete snapshot fails closed.
func effectiveConfigFromFrozenDownloadPolicy(frozenPolicy []byte) (effectiveConfig, error) {
	if len(frozenPolicy) == 0 || string(frozenPolicy) == "null" {
		return effectiveConfig{}, fmt.Errorf("%w: the download task has no frozen policy", ErrFrozenConfigRequired)
	}
	var frozen struct {
		EffectiveSections map[string]json.RawMessage   `json:"effective_sections"`
		SectionVersions   map[string]int64             `json:"section_versions"`
		Vars              *namingvars.FrozenEvaluation `json:"vars"`
	}
	if err := json.Unmarshal(frozenPolicy, &frozen); err != nil {
		return effectiveConfig{}, fmt.Errorf("%w: decode frozen policy: %v", ErrFrozenConfigRequired, err)
	}
	required := []string{
		animedomain.SectionParser, animedomain.SectionFileRules,
		animedomain.SectionNamingRules, animedomain.SectionFileEffect,
	}
	for _, section := range required {
		if raw, ok := frozen.EffectiveSections[section]; !ok || len(raw) == 0 || string(raw) == "null" {
			return effectiveConfig{}, fmt.Errorf("%w: frozen policy is missing section %q", ErrFrozenConfigRequired, section)
		}
	}
	var parser fileOperationParserRules
	if err := json.Unmarshal(frozen.EffectiveSections[animedomain.SectionParser], &parser); err != nil {
		return effectiveConfig{}, fmt.Errorf("%w: decode frozen parser rules: %v", ErrFrozenConfigRequired, err)
	}
	var fileFilter fileOperationFileFilterRules
	if err := json.Unmarshal(frozen.EffectiveSections[animedomain.SectionFileRules], &fileFilter); err != nil {
		return effectiveConfig{}, fmt.Errorf("%w: decode frozen file filter rules: %v", ErrFrozenConfigRequired, err)
	}
	var namingRules naming.NamingRules
	if err := json.Unmarshal(frozen.EffectiveSections[animedomain.SectionNamingRules], &namingRules); err != nil {
		return effectiveConfig{}, fmt.Errorf("%w: decode frozen naming rules: %v", ErrFrozenConfigRequired, err)
	}
	var effect fileOperationEffectRules
	if err := json.Unmarshal(frozen.EffectiveSections[animedomain.SectionFileEffect], &effect); err != nil {
		return effectiveConfig{}, fmt.Errorf("%w: decode frozen file effect rules: %v", ErrFrozenConfigRequired, err)
	}
	versions := map[string]int64{}
	for key, value := range frozen.SectionVersions {
		versions[key] = value
	}
	return effectiveConfig{
		vars:       frozen.Vars,
		naming:     namingRules,
		parser:     parser,
		effect:     effect,
		fileFilter: fileFilter,
		versions:   versions,
	}, nil
}

func collisionPolicyToDB(policy fileOperationCollisionPolicy) (string, bool) {
	switch policy {
	case fileOperationCollisionFail:
		return "block", true
	case fileOperationCollisionVersioned:
		return "version", true
	case fileOperationCollisionOverwrite:
		return "overwrite", true
	default:
		return "", false
	}
}

// marshalConfigSnapshot freezes the effective rules, section versions, and the
// parser provenance read from the Release media fact snapshot used at plan
// time. Targets are absolute, so there is no library anchor to freeze. Nil
// provenance leaves the key out entirely: a release without a fact snapshot
// must not gain invented parser evidence.
func marshalConfigSnapshot(config effectiveConfig, validation map[string]any, parserProvenance *parser.Provenance) (json.RawMessage, error) {
	sections := map[string]int64{}
	for key, value := range config.versions {
		sections[key] = value
	}
	snapshot := map[string]any{
		"rules_version":      maxVersion(config.versions[animedomain.SectionNamingRules]),
		"safety_version":     animedomain.SafetyVersion,
		"effective_versions": sections,
		"naming_rules":       config.naming,
		"vars":               config.vars,
		"file_effect":        config.effect,
	}
	if config.vars != nil {
		snapshot["vars"] = map[string]any{"program": config.vars.Program, "input": config.vars.Input, "output": config.vars.Output, "status": config.vars.Status, "diagnostic": config.vars.Diagnostic}
	}
	if parserProvenance != nil {
		snapshot["parser"] = *parserProvenance
	}
	if validation != nil {
		snapshot["validation"] = validation
	}
	return json.Marshal(snapshot)
}

// renderBindingFacts records the metadata binding the naming render actually
// read, so the frozen render snapshot is reproducible.
//
// The snapshot already freezes the rendered output (directory, filename,
// target). Without the inputs behind it a maintenance re-render cannot
// reproduce that output: the release media-fact snapshot is frozen when the
// release is evaluated, so a TMDB binding set between that evaluation and the
// file effect is invisible there, while the effect itself read the current
// metadata row. The render then either drops the season folder or fails as
// unavailable, and path-diff/alignment disagrees with the committed placement.
// These four facts are exactly what naming reads from the metadata binding.
func renderBindingFacts(facts naming.NamingContext) map[string]any {
	return map[string]any{
		"media_kind":      facts.Anime.TMDB.MediaKind,
		"season":          facts.Anime.TMDB.Season,
		"season_bound":    facts.Anime.TMDB.SeasonPresent,
		"effective_title": facts.Derived.EffectiveTitle,
		// The owner-edited year (anime.year) the render read.
		"anime_year": facts.Anime.Year,
		// The owner-edited season (anime.season) the render read; tmdb's
		// season above stays the raw binding.
		"anime_season": facts.Anime.Season,
		"extension":    facts.File.Extension,
		"tmdb_work_id": facts.Anime.TMDB.WorkID,
		"bgm_work_id":  facts.Anime.BGM.WorkID,
		// The frozen native titles, provider years and torrent metadata ride
		// along verbatim, so a maintenance re-render reads the values this render read.
		"bgm_title":           facts.Anime.BGM.Title,
		"bgm_title_cn":        facts.Anime.BGM.TitleCN,
		"bgm_year":            facts.Anime.BGM.Year,
		"tmdb_title_cn":       facts.Anime.TMDB.TitleCN,
		"tmdb_original_title": facts.Anime.TMDB.OriginalTitle,
		"tmdb_year":           facts.Anime.TMDB.Year,
		"torrent_size":        facts.Torrent.Size,
		"torrent_file_count":  facts.Torrent.FileCount,
		"torrent_hash_v1":     facts.Torrent.HashV1,
		"torrent_hash_v2":     facts.Torrent.HashV2,
	}
}

// bindingYear narrows a frozen provider year into the naming projection type.
// A value outside the supported provider range is not an air/release year and
// is treated as unknown, the same rule the accept-side normalizer applies.
func bindingYear(value *int64) *int {
	if value == nil {
		return nil
	}
	year := int(*value)
	if year < templatefacts.MinYear || year > templatefacts.MaxYear {
		return nil
	}
	return &year
}

// frozenSeasonColumn projects the frozen media binding's season onto the
// artifact's season column: a real season or a preserved season 0 (continuous
// SP) is a bound season; the unbound 0 the binding carries otherwise is NULL,
// and naming.AnimeSeason then names the unbound default, exactly as the
// pre-download target did.
func frozenSeasonColumn(season int, seasonBound bool) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(season), Valid: season >= 1 || seasonBound}
}

// buildNamingContext assembles the Layer 1 naming facts for one artifact
// file and resolves Parsed/Derived through the default resolver registry.
// It performs no I/O and no implicit metadata lookups. The frozen episode of
// the file is the only episode authority; without one parsed.episode is null.
func buildNamingContext(artifact artifactContext, file artifactFileContext, frozenEpisode *naming.FrozenEpisodeNamingFacts, evaluations ...*namingvars.FrozenEvaluation) (naming.ResolvedContext, error) {
	facts, err := namingContextFacts(artifact, file, frozenEpisode, evaluations...)
	if err != nil {
		return naming.ResolvedContext{}, err
	}
	return naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: facts}), nil
}

// namingContextFacts is buildNamingContext before resolution: the Layer 1
// facts alone, which the configuration gate resolves again with the anime's
// current configuration in place of the frozen one.
func namingContextFacts(artifact artifactContext, file artifactFileContext, frozenEpisode *naming.FrozenEpisodeNamingFacts, evaluations ...*namingvars.FrozenEvaluation) (naming.NamingContext, error) {
	var ctx naming.NamingContext

	animeTitle := strings.TrimSpace(artifact.animeTitle)
	ctx.Anime.ID = artifact.managedAnimeID
	if artifact.titleOverride.Valid && strings.TrimSpace(artifact.titleOverride.String) != "" {
		ctx.Anime.TitleOverride = strings.TrimSpace(artifact.titleOverride.String)
	}
	ctx.Anime.Title = animeTitle
	ctx.Anime.ProviderTitle = strings.TrimSpace(artifact.providerTitle)
	ctx.Source.Kind = artifact.sourceKind
	ctx.Source.GroupName = strings.TrimSpace(artifact.groupName)
	ctx.Source.Group = providerdomain.GroupRefOrZero(providerdomain.Kind(artifact.sourceProvider), artifact.sourceGroupRef)
	if artifact.metadataBGM.Valid {
		ctx.Anime.BGM.WorkID = artifact.metadataBGM.String
	}
	if artifact.tmdbWorkID.Valid {
		ctx.Anime.TMDB.WorkID = artifact.tmdbWorkID.String
	}
	if artifact.tmdbMediaKind.Valid {
		ctx.Anime.TMDB.MediaKind = artifact.tmdbMediaKind.String
	}
	if artifact.tmdbSeason.Valid {
		// Valid already means a real season or a preserved season 0 (prepare).
		ctx.Anime.TMDB.Season = int(artifact.tmdbSeason.Int64)
		ctx.Anime.TMDB.SeasonPresent = true
		ctx.Anime.TMDB.SeasonBound = artifact.tmdbSeason.Int64 == 0
	}
	ctx.Anime.BGM.Title, ctx.Anime.BGM.TitleCN = artifact.bindingTitles.bgmTitle, artifact.bindingTitles.bgmTitleCN
	ctx.Anime.BGM.Year = bindingYear(artifact.bindingTitles.bgmYear)
	ctx.Anime.TMDB.TitleCN, ctx.Anime.TMDB.OriginalTitle = artifact.bindingTitles.tmdbTitleCN, artifact.bindingTitles.tmdbOriginalTitle
	ctx.Anime.TMDB.Year = bindingYear(artifact.bindingTitles.tmdbYear)
	ctx.Torrent = artifact.torrent

	ctx.Release.ID = artifact.releaseID
	ctx.Release.Version = int(artifact.releaseVersion)
	ctx.Release.FrozenEpisode = frozenEpisode

	base := filepath.Base(file.sourcePath)
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), ".")
	ctx.File = naming.FileNamingFacts{
		OriginalName: base,
		Basename:     strings.TrimSuffix(base, filepath.Ext(base)),
		Extension:    extension,
		SizeBytes:    file.sizeBytes,
		Kind:         file.mediaKind,
	}

	var rawFacts struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(artifact.rawFacts, &rawFacts) == nil {
		ctx.Release.OriginalTitle = strings.TrimSpace(rawFacts.Title)
	}

	var mediaFacts map[string]any
	if json.Unmarshal(file.facts, &mediaFacts) == nil {
		ctx.File.Media.Resolution = mediaFactString(mediaFacts, "resolution")
		ctx.File.Media.VideoCodec = mediaFactString(mediaFacts, "video_codec")
		ctx.File.Media.AudioCodec = mediaFactString(mediaFacts, "audio_codec")
		ctx.File.Media.Container = mediaFactString(mediaFacts, "container")
		ctx.File.Media.HDR = mediaFactString(mediaFacts, "hdr")
	}

	// Release-level quality is the shared fallback for missing file facts
	// (the downloaded filename usually carries no [1080p]/language markers):
	// parsing the release title keeps the naming render equal to the
	// reconciliation planner, which consumes the same release facts.
	// Accepted (typed) plans must not re-parse with today's attribute rules:
	// they take the release resolution frozen in the accepted media.quality
	// (else on the release media fact snapshot) — the same value the
	// pre-download TargetSet rendered with.
	if artifact.acceptedFacts && ctx.File.Media.Resolution == "" {
		ctx.File.Media.Resolution = artifact.acceptedResolution
		if ctx.File.Media.Resolution == "" {
			// Inputs accepted before media.quality was frozen.
			ctx.File.Media.Resolution = artifact.frozenReleaseResolution
		}
	}
	if !artifact.acceptedFacts && ctx.File.Media.Resolution == "" && strings.TrimSpace(ctx.Release.OriginalTitle) != "" {
		rules, _, err := attrrules.Current()
		if err != nil {
			return naming.NamingContext{}, err
		}
		if parsed := acquisitiondomain.ParseReleaseFacts(ctx.Release.OriginalTitle, rules); parsed.Resolution != "" {
			ctx.File.Media.Resolution = parsed.Resolution
		}
	}

	// The value namespaces are the Release's frozen source facts, the same
	// block the pre-download TargetSet rendered with.
	namespaces, err := templatefacts.FromSourceFacts(artifact.sourceFacts)
	if err != nil {
		return naming.NamingContext{}, err
	}
	ctx = naming.WithTemplateFacts(ctx, namespaces)

	if len(evaluations) == 0 || evaluations[0] == nil || evaluations[0].Status != "ok" {
		return naming.NamingContext{}, fmt.Errorf("exact frozen Vars output required")
	}
	// anime.year is the owner-edited year the acceptance froze with the Vars
	// input, the value the pre-download target rendered with.
	ctx.Anime.Year = naming.FrozenAnimeYear(evaluations[0].Input.Facts)
	// anime.season is the owner-edited season frozen the same way; an input
	// accepted before it was frozen names the season its binding named.
	ctx.Anime.Season = naming.FrozenAnimeSeason(evaluations[0].Input.Facts, ctx.Anime.TMDB)
	return naming.WithVars(ctx, evaluations[0].Output)
}

func mediaFactString(facts map[string]any, key string) string {
	value, ok := facts[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stableIdempotencyKey(fileID string, artifactVersion int64) string {
	key := "file_execute:" + fileID + ":" + strconv.FormatInt(artifactVersion, 10)
	return stableBoundedKey(key)
}
