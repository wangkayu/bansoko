package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	"bansoko/internal/parser"
	"bansoko/internal/release/facts"
	rssapplication "bansoko/internal/rss/application"
)

// fileEpisodeParser reads the episode badge of one release filename. It is
// the release's frozen parser selection, never a default pattern; nil means
// the release froze no parser and the file carries no episode badge.
type fileEpisodeParser func(filename string) string

// frozenEpisodeParser builds the badge parser from the parser kind and config
// frozen on the release's media fact snapshot. A range, list or unparsable
// filename has no scalar episode and yields no badge. The badge is the raw
// capture: it describes the file, so offsets do not apply.
func frozenEpisodeParser(kind string, config []byte) fileEpisodeParser {
	if kind == "" {
		return nil
	}
	selection := parser.ParserSelection{ID: kind, Config: config}
	return func(filename string) string {
		result, err := parser.ParseEpisode(context.Background(), parser.DefaultRegistry(), selection, parser.EpisodeParseInput{Kind: parser.InputFileName, Text: filename})
		if err != nil || result.Status != parser.StatusValue || result.Episode == nil {
			return ""
		}
		return string(*result.Episode)
	}
}

type releaseInternalFacts struct {
	ReleaseFiles []releaseInternalFile `json:"release_files,omitempty"`
}

type releaseInternalFile struct {
	SourcePath       string                              `json:"source_path"`
	Filename         string                              `json:"filename"`
	SizeBytes        *int64                              `json:"size_bytes,omitempty"`
	MediaKind        string                              `json:"media_kind,omitempty"`
	TargetPath       *string                             `json:"target_path,omitempty"`
	TargetDiagnostic *acquisitiondomain.TargetDiagnostic `json:"target_diagnostic,omitempty"`
}

type releaseFileBadge struct {
	episode    string
	resolution string
	languages  []string
	revision   string
}

// releaseTargetSet is the target set (and the latest observation of each
// target) of one release.
type releaseTargetSet struct {
	targets      []AcquisitionTarget
	observations []TargetObservation
}

// pendingRelease is a release row whose file badges are not projected yet: the
// badges of all releases are read with one query once every row is scanned.
type pendingRelease struct {
	release      Release
	files        []releaseInternalFile
	filesDecoded bool
	snapshotID   sql.NullString
	parserKind   string
	parserConfig []byte
	batchPlanRaw []byte
}

// releaseReadOptions shapes one releases read.
type releaseReadOptions struct {
	// perSourceLimit > 0 reads only that many newest releases of each source (the
	// RSS list view never renders more); 0 reads them all.
	perSourceLimit int
	// targets, when not nil, supplies each release's target set.
	targets map[string]releaseTargetSet
	// vars additionally reads what the Vars evidence of a persisted release needs
	// (its frozen source facts and torrent input hashes), so the list view builds
	// it without two more queries per release.
	vars bool
	// fileIdentity additionally reads each release's torrent payload digest, the
	// binding every file_ref is derived from. The RSS list view rebuilds an owner
	// release's manifest and needs it; a reader that renders no file identity
	// does not pay for the join.
	fileIdentity bool
}

// readReleasesApplication reads the releases of one subscription, newest first.
func (s *Service) readReleasesApplication(ctx context.Context, subscriptionID string, options releaseReadOptions) ([]Release, error) {
	perSourceLimit, targets := options.perSourceLimit, options.targets
	// An owner release's manifest is rebuilt from its frozen file facts, and
	// every file_ref is derived from the payload digest of the torrent those
	// facts were parsed from. The RSS list view asks for that digest
	// (fileIdentity); a reader that never projects file identities — the full
	// workspace — skips the join. The info hashes are Vars evidence and are read
	// only when asked.
	digestColumn := `''`
	hashColumns := `'', ''`
	hashJoin := ``
	if options.fileIdentity || options.vars {
		digestColumn = `COALESCE(rti.payload_digest, '')`
		hashJoin = `
LEFT JOIN LATERAL (
    SELECT payload_digest, info_hash_v1, info_hash_v2 FROM release_torrent_inputs WHERE release_id = r.id LIMIT 1
) AS rti ON true`
	}
	if options.vars {
		hashColumns = `COALESCE(rti.info_hash_v1, ''), COALESCE(rti.info_hash_v2, '')`
	}
	from := `
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id`
	args := []any{subscriptionID, acquisitionAccessScope}
	if perSourceLimit > 0 {
		from = `
FROM rss_sources src
CROSS JOIN LATERAL (
    SELECT * FROM releases
    WHERE rss_source_id = src.id
    ORDER BY (CASE WHEN raw_facts->>'published_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}' THEN (raw_facts->>'published_at')::timestamptz ELSE discovered_at END) DESC, id DESC
    LIMIT $3
) AS r`
		args = append(args, perSourceLimit)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id, r.rss_source_id, r.stable_item_key, r.raw_facts, r.source_facts, r.parsed_facts, r.internal_facts,
       r.verdict, r.reason_code, r.reason_detail, r.evaluation_version, r.evaluated_rules_version,
       r.download_task_id, r.version, snap.id, snap.parser_kind, snap.parser_config, snap.batch_plan, acq.id,
       COALESCE(cur_task.state, ''), COALESCE(cur_acq.state, ''),
       CASE WHEN cur_task.state = 'completed' THEN `+rssapplication.ReleasePlacementIssueSQL("(SELECT owner_sub.managed_anime_id FROM subscriptions owner_sub WHERE owner_sub.id = src.subscription_id)")+` ELSE false END,
       `+digestColumn+`, `+hashColumns+from+hashJoin+`
LEFT JOIN download_tasks cur_task ON cur_task.id = r.download_task_id
LEFT JOIN acquisitions cur_acq ON cur_acq.id = cur_task.acquisition_id
LEFT JOIN LATERAL (
    SELECT id, parser_kind, parser_config, release_facts->'batch_plan' AS batch_plan FROM media_fact_snapshots
    WHERE release_id = r.id
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) AS snap ON true
LEFT JOIN LATERAL (
    SELECT a.id FROM acquisitions a
    WHERE a.source_release_ref = r.id AND a.access_scope = $2
    ORDER BY a.retention_anchor_at DESC, a.id DESC
    LIMIT 1
) AS acq ON true
WHERE src.subscription_id = $1
	ORDER BY (CASE WHEN r.raw_facts->>'published_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}' THEN (r.raw_facts->>'published_at')::timestamptz ELSE r.discovered_at END) DESC, r.id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("read releases: %w", err)
	}
	defer rows.Close()
	var pending []pendingRelease
	for rows.Next() {
		var item pendingRelease
		release := &item.release
		var rawFacts, sourceFacts, parsedFacts, internalFacts []byte
		var reasonDetail, evaluationVersion, downloadTaskID sql.NullString
		var evaluatedRulesVersion sql.NullInt64
		var snapshotParserKind, acquisitionID sql.NullString
		if err := rows.Scan(&release.ID, &release.RSSSourceID, &release.StableItemKey, &rawFacts, &sourceFacts, &parsedFacts, &internalFacts,
			&release.Verdict, &release.ReasonCode, &reasonDetail, &evaluationVersion, &evaluatedRulesVersion, &downloadTaskID, &release.Version, &item.snapshotID, &snapshotParserKind, &item.parserConfig, &item.batchPlanRaw, &acquisitionID,
			&release.currentTaskState, &release.currentAcquisitionState, &release.placementMissing, &release.payloadDigest, &release.inputHashV1, &release.inputHashV2); err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		if options.vars {
			release.rawSourceFacts = append([]byte(nil), sourceFacts...)
		}
		item.parserKind = snapshotParserKind.String
		release.InternalFacts = append([]byte(nil), internalFacts...)
		release.AcquisitionID = nullableStringPointer(acquisitionID)
		if evaluatedRulesVersion.Valid {
			release.EvaluatedRulesVersion = evaluatedRulesVersion.Int64
		}
		if err := json.Unmarshal(rawFacts, &release.RawFacts); err != nil {
			return nil, fmt.Errorf("decode release raw facts: %w", err)
		}
		if err := json.Unmarshal(sourceFacts, &release.SourceFacts); err != nil {
			return nil, fmt.Errorf("decode release source facts: %w", err)
		}
		if parsedFacts != nil {
			var value ReleaseParsedFacts
			if err := json.Unmarshal(parsedFacts, &value); err == nil {
				release.ParsedFacts = &value
			}
		}
		if len(internalFacts) > 0 {
			var internal releaseInternalFacts
			if err := json.Unmarshal(internalFacts, &internal); err == nil {
				item.files, item.filesDecoded = internal.ReleaseFiles, true
			}
		}
		if reasonDetail.Valid {
			value := reasonDetail.String
			release.ReasonDetail = &value
		}
		if evaluationVersion.Valid {
			value := evaluationVersion.String
			release.EvaluationVersion = &value
		}
		if downloadTaskID.Valid {
			value := downloadTaskID.String
			release.DownloadTaskID = &value
		}
		if set, ok := targets[release.ID]; ok {
			if len(set.targets) > 0 {
				targets := set.targets
				release.AcquisitionTargets = &targets
			}
			if len(set.observations) > 0 {
				observations := set.observations
				release.TargetObservations = &observations
			}
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate releases: %w", err)
	}
	rows.Close()

	badgeRows := s.readFileRowsBySnapshots(ctx, pending)
	result := make([]Release, 0, len(pending))
	for _, item := range pending {
		release := item.release
		if item.filesDecoded {
			release.ReleaseFiles = s.projectReleaseFiles(item.files, badgeRows[item.snapshotID.String], frozenEpisodeParser(item.parserKind, item.parserConfig))
			applyPlanOutcomes(release.ReleaseFiles, item.batchPlanRaw)
			release.frozenPlan = decodeFrozenPlan(item.batchPlanRaw)
		}
		result = append(result, release)
	}
	return result, nil
}

// readFileRowsBySnapshots reads the frozen per-file facts of every release that
// lists release files, in one query. Like the single-snapshot read it replaces,
// a failure only costs the frozen badges: each file then falls back to the
// display parse of its name.
func (s *Service) readFileRowsBySnapshots(ctx context.Context, pending []pendingRelease) map[string][]facts.FileRow {
	if s.mediaFacts == nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, item := range pending {
		if len(item.files) == 0 || !item.snapshotID.Valid || item.snapshotID.String == "" || seen[item.snapshotID.String] {
			continue
		}
		seen[item.snapshotID.String] = true
		ids = append(ids, item.snapshotID.String)
	}
	if len(ids) == 0 {
		return nil
	}
	if rows, err := s.mediaFacts.ListFileRowsBySnapshots(ctx, ids); err == nil {
		return rows
	}
	rows := make(map[string][]facts.FileRow, len(ids))
	for _, id := range ids {
		if value, err := s.mediaFacts.ListFileRows(ctx, id); err == nil {
			rows[id] = value
		}
	}
	return rows
}

// readAcquisitionTargetsApplication reads the target set of every release of a
// subscription with one query. It returns the subscription-wide target and
// observation lists of the workspace (targets by release, target_ref; the
// observations newest first) and the same rows grouped by release.
func (s *Service) readAcquisitionTargetsApplication(ctx context.Context, subscriptionID string) ([]AcquisitionTarget, []TargetObservation, map[string]releaseTargetSet, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT at.id, at.release_id, at.target_ref, at.source_path, at.media_kind,
       at.target_path, at.expected_size_bytes, at.intent, at.collision_policy,
       at.spec_digest, at.version,
       at.observation_state, at.observed_size_bytes, at.observation_reason_code, at.observed_at, at.observation_version,
       row_number() OVER (ORDER BY at.observed_at DESC, at.id)
FROM acquisition_targets at
JOIN releases r ON r.id = at.release_id
JOIN rss_sources src ON src.id = r.rss_source_id
WHERE src.subscription_id = $1
ORDER BY at.release_id, at.target_ref, at.id`, subscriptionID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read acquisition targets: %w", err)
	}
	defer rows.Close()
	type rankedObservation struct {
		rank        int64
		observation TargetObservation
	}
	var ranked []rankedObservation
	targets := make([]AcquisitionTarget, 0)
	byRelease := map[string]releaseTargetSet{}
	for rows.Next() {
		var target AcquisitionTarget
		var state, reason sql.NullString
		var observedSize, observationVersion sql.NullInt64
		var observedAt sql.NullTime
		var rank int64
		if err := rows.Scan(&target.ID, &target.ReleaseID, &target.TargetRef, &target.SourcePath, &target.MediaKind,
			&target.TargetPath, &target.ExpectedSizeBytes, &target.Intent, &target.CollisionPolicy, &target.SpecDigest, &target.Version,
			&state, &observedSize, &reason, &observedAt, &observationVersion, &rank); err != nil {
			return nil, nil, nil, fmt.Errorf("scan acquisition target: %w", err)
		}
		targets = append(targets, target)
		set := byRelease[target.ReleaseID]
		set.targets = append(set.targets, target)
		if state.Valid {
			observation := TargetObservation{TargetID: target.ID, State: state.String}
			if observedSize.Valid {
				value := observedSize.Int64
				observation.ObservedSizeBytes = &value
			}
			if reason.Valid {
				value := reason.String
				observation.ReasonCode = &value
			}
			if observedAt.Valid {
				observation.ObservedAt = observedAt.Time
			}
			if observationVersion.Valid {
				observation.Version = observationVersion.Int64
			}
			set.observations = append(set.observations, observation)
			ranked = append(ranked, rankedObservation{rank: rank, observation: copyTargetObservation(observation)})
		}
		byRelease[target.ReleaseID] = set
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterate acquisition targets: %w", err)
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].rank < ranked[j].rank })
	observations := make([]TargetObservation, 0, len(ranked))
	for _, item := range ranked {
		observations = append(observations, item.observation)
	}
	return targets, observations, byRelease, nil
}

func copyTargetObservation(value TargetObservation) TargetObservation {
	value.ObservedSizeBytes = cloneWorkspaceInt64(value.ObservedSizeBytes)
	value.ReasonCode = cloneWorkspaceString(value.ReasonCode)
	return value
}

func (s *Service) releaseFilesFromInternalApplication(files []releaseInternalFile, snapshotID sql.NullString, episodes fileEpisodeParser) *[]ReleaseFile {
	if len(files) == 0 {
		return nil
	}
	var rows []facts.FileRow
	if s.mediaFacts != nil && snapshotID.Valid && snapshotID.String != "" {
		if value, err := s.mediaFacts.ListFileRows(context.Background(), snapshotID.String); err == nil {
			rows = value
		}
	}
	return s.projectReleaseFiles(files, rows, episodes)
}

// projectReleaseFiles shows each file of a release with the badges frozen on
// its media fact snapshot (rows), or, for a file the snapshot does not name,
// the display parse of its filename.
func (s *Service) projectReleaseFiles(files []releaseInternalFile, rows []facts.FileRow, episodes fileEpisodeParser) *[]ReleaseFile {
	if len(files) == 0 {
		return nil
	}
	badges := map[string]releaseFileBadge{}
	for _, row := range rows {
		badge := releaseFileBadge{}
		if row.EffectiveFacts.Identity.State == "resolved" && row.EffectiveFacts.Identity.Episode != nil {
			badge.episode = string(*row.EffectiveFacts.Identity.Episode)
			// A multi-episode file shows its range ("1-2").
			if end := row.EffectiveFacts.Identity.EpisodeEnd; end != nil {
				badge.episode += "-" + string(*end)
			}
		}
		badge.resolution = row.FileEvidence.Resolution
		badge.languages = row.FileEvidence.Languages
		badge.revision = row.FileEvidence.Revision
		badges[row.FileEvidence.Name] = badge
	}
	result := make([]ReleaseFile, 0, len(files))
	for _, file := range files {
		projected := ReleaseFile{
			Episode:          nil,
			Filename:         file.Filename,
			Language:         nil,
			MediaKind:        file.MediaKind,
			Path:             file.SourcePath,
			Resolution:       nil,
			Revision:         nil,
			SizeBytes:        cloneWorkspaceInt64(file.SizeBytes),
			TargetPath:       cloneWorkspaceString(file.TargetPath),
			TargetDiagnostic: mapWorkspaceTargetDiagnostic(file.TargetDiagnostic),
		}
		badge, frozen := badges[file.Filename]
		if frozen {
			projected.Episode = optionalWorkspaceString(badge.episode)
			projected.Resolution = optionalWorkspaceString(badge.resolution)
			projected.Revision = optionalWorkspaceString(badge.revision)
			if len(badge.languages) > 0 {
				value := badge.languages[0]
				projected.Language = &value
			}
			result = append(result, projected)
			continue
		}
		releaseRules, _, err := attrrules.Current()
		if err != nil {
			return nil
		}
		display := acquisitiondomain.ParseFileDisplayFacts(file.Filename, releaseRules)
		if episodes != nil {
			projected.Episode = optionalWorkspaceString(episodes(file.Filename))
		}
		projected.Resolution = optionalWorkspaceString(display.Resolution)
		projected.Revision = optionalWorkspaceString(display.Revision)
		projected.Language = optionalWorkspaceString(display.Language)
		result = append(result, projected)
	}
	return &result
}

func cloneWorkspaceInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneWorkspaceString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func mapWorkspaceTargetDiagnostic(value *acquisitiondomain.TargetDiagnostic) *TargetDiagnostic {
	if value == nil {
		return nil
	}
	return &TargetDiagnostic{Code: value.Code, Message: value.Message, SourcePath: optionalWorkspaceString(value.SourcePath)}
}

func optionalWorkspaceString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// decodeFrozenPlan reads the batch plan frozen on the latest media fact
// snapshot; nil when the snapshot carries none.
func decodeFrozenPlan(raw []byte) *batchplan.Plan {
	if len(raw) == 0 {
		return nil
	}
	var plan batchplan.Plan
	if json.Unmarshal(raw, &plan) != nil || plan.Version == "" {
		return nil
	}
	return &plan
}

// applyPlanOutcomes copies each file's conclusion from the batch plan the
// release was accepted with (frozen on its media fact snapshot), with the
// reason of a file it does not place. A release without a plan, or a file the
// plan does not name, carries no outcome.
func applyPlanOutcomes(files *[]ReleaseFile, raw []byte) {
	plan := decodeFrozenPlan(raw)
	if files == nil || plan == nil {
		return
	}
	for index := range *files {
		file := &(*files)[index]
		entry, ok := plan.File(file.Path)
		if !ok || entry.Outcome == "" {
			continue
		}
		outcome := string(entry.Outcome)
		file.Outcome = &outcome
		// Only a file the plan does not place carries a reason (an unknown
		// video kept under the route is placed, though unrecognized).
		if entry.ReasonCode != "" && !plan.Required(file.Path) {
			reason := entry.ReasonCode
			file.OutcomeReason = &reason
		}
	}
}
