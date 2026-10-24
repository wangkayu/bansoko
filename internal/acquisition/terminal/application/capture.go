package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	anime "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/anime/metadatabinding"
	"bansoko/internal/attrrules"
	catalogdb "bansoko/internal/catalog/adapter/persistence"
	catalogreader "bansoko/internal/catalog/application"
	"bansoko/internal/namingvars"
)

// Only package-owned intent resolution may assemble these requests. No exported
// service, transport DTO, arbitrary query, callback, or clock is accepted.
type qualificationRequest struct {
	configKind, releaseID, candidateID, requestHash string
	entryMode                                       string
	revision                                        int64
	catalogKind                                     string // fixed intent: provider_snapshot or release_input
	submitted                                       *anime.BusinessSnapshot
	submittedDraft                                  *anime.AnimeDraft
	catalog                                         *catalogRequest
	ownerResolution                                 *OwnerResolution
}
type catalogRequest struct{ observationID, feedID, seedKey string }

// captureQualification performs fixed SELECTs in a read-only snapshot. Exact
// feed and Vars readers own their reads separately; their immutable identities
// are rechecked by 132 at acceptance, not authorized by this capture transaction.
func captureQualification(ctx context.Context, db *sql.DB, revisions *draft.Service, req qualificationRequest) (admissionCapture, error) {
	if db == nil || revisions == nil || req.releaseID == "" || req.requestHash == "" {
		return admissionCapture{}, materialError("request", nil)
	}
	if (req.catalogKind != "provider_snapshot" && req.catalogKind != "release_input") || (req.catalogKind == "provider_snapshot") != (req.catalog != nil) {
		return admissionCapture{}, materialError("catalog", nil)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return admissionCapture{}, materialError("database", err)
	}
	defer tx.Rollback()
	captured, err := captureQualificationTx(ctx, tx, revisions, req)
	if err != nil {
		return admissionCapture{}, err
	}
	if err := tx.Commit(); err != nil {
		return admissionCapture{}, materialError("database", err)
	}
	return captured, nil
}

func captureQualificationTx(ctx context.Context, tx *sql.Tx, revisions *draft.Service, req qualificationRequest) (admissionCapture, error) {
	if tx == nil || revisions == nil {
		return admissionCapture{}, materialError("database", nil)
	}
	var err error
	m := qualificationMaterial{entryMode: req.entryMode, config: configMaterial{kind: req.configKind, requestHash: req.requestHash}, catalogKind: req.catalogKind}
	mg, r := &m.management, &m.release
	var lifecycleState, subscriptionState string
	var task sql.NullString
	var rawTitleOverride, rawProviderTitle sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.version,r.rss_source_id,src.version,src.episode_offset,src.subscription_id,
 sub.managed_anime_id,sub.id,sub.version,sub.state,lc.lifecycle_version,lc.lifecycle_state,r.download_task_id,lc.title,lc.title_override,lc.provider_title,src.group_name
 FROM releases r JOIN rss_sources src ON src.id=r.rss_source_id
 JOIN subscriptions sub ON sub.id=src.subscription_id
 JOIN managed_anime lc ON lc.id=sub.managed_anime_id
 WHERE r.id=$1`, req.releaseID).Scan(&r.id, &r.version, &r.sourceID, &r.sourceVersion, &r.offset, &r.subscriptionID,
		&mg.managedID, &mg.subscriptionID, &mg.subscriptionVersion, &subscriptionState, &mg.lifecycleVersion, &lifecycleState, &task, &mg.title, &rawTitleOverride, &rawProviderTitle, &r.groupName)
	if err != nil {
		return admissionCapture{}, materialError("source", err)
	}
	// The managed anime's own titles are captured with the same row: the
	// acceptance freezes the override and the provider catalog title, and both the
	// Vars input and the typed plan read them back.
	mg.titleOverride, mg.providerTitle = strings.TrimSpace(rawTitleOverride.String), strings.TrimSpace(rawProviderTitle.String)
	m.managementNone = (req.entryMode == "one_shot_seed" || req.entryMode == "one_shot_snapshot")
	if m.managementNone && req.configKind == "owner" {
		return admissionCapture{}, materialError("config", nil)
	}
	if !m.managementNone && (lifecycleState != "active" || (subscriptionState != "active" && !(req.entryMode == "subscription_direct" && subscriptionState == "stopped"))) {
		return admissionCapture{}, materialError("management", nil)
	}
	if task.Valid {
		return admissionCapture{}, materialError("source", nil)
	}
	switch req.configKind {
	case "owner":
		if req.submitted != nil || req.candidateID != "" || req.revision != 0 {
			return admissionCapture{}, materialError("config", nil)
		}
		var raw []byte
		m.config.managedID = mg.managedID
		m.ownerResolution = req.ownerResolution
		err = tx.QueryRowContext(ctx, `SELECT business_version,business_snapshot,business_digest FROM anime_business_configs WHERE managed_anime_id=$1 AND business_snapshot IS NOT NULL`, mg.managedID).Scan(&m.config.version, &raw, &m.config.digest)
		if err == nil {
			err = json.Unmarshal(raw, &m.config.business)
		}
	case "saved_revision":
		if (req.submitted != nil && !m.managementNone) || req.candidateID == "" || req.revision < 1 {
			return admissionCapture{}, materialError("config", nil)
		}
		var current sql.NullInt64
		var pointer sql.NullString
		var status string
		var activated sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT version,current_revision,current_revision_digest,status,activated_managed_anime_id FROM anime_config_candidates WHERE id=$1`, req.candidateID).Scan(&m.config.version, &current, &pointer, &status, &activated)
		if err == nil && (!current.Valid || current.Int64 != req.revision || !pointer.Valid || status != "working" || activated.Valid) {
			return admissionCapture{}, materialError("config", nil)
		}
		if err == nil {
			var saved draft.CandidateRevision
			saved, _, err = revisions.LoadCandidateRevisionTx(ctx, tx, req.candidateID, req.revision)
			if err == nil {
				if saved.EffectiveDigest != pointer.String || (saved.Business != nil && saved.BusinessDigest == nil) {
					return admissionCapture{}, materialError("config", nil)
				}
				if saved.Business != nil {
					m.config.business, m.config.digest = *saved.Business, *saved.BusinessDigest
				} else {
					if !m.managementNone || req.submitted == nil {
						return admissionCapture{}, materialError("config", nil)
					}
					m.config.business, err = req.submitted.Freeze()
					if err != nil {
						return admissionCapture{}, materialError("config", err)
					}
					m.config.digest, err = m.config.business.Digest()
					if err != nil {
						return admissionCapture{}, materialError("config", err)
					}
				}
				m.bgm, m.tmdb = saved.Draft.Metadata.Bgm, saved.Draft.Metadata.Tmdb
				m.mediaKindOverride = saved.Draft.Metadata.MediaKindOverride
				m.animeYear = saved.Draft.Metadata.Year
				m.animeSeason = saved.Draft.Metadata.Season
				m.config.candidateID, m.config.revision, m.config.revisionDigest = saved.CandidateID, saved.Revision, saved.EffectiveDigest
			}
		}
	case "submitted":
		if req.submitted == nil || req.candidateID != "" || req.revision != 0 {
			return admissionCapture{}, materialError("config", nil)
		}
		if req.submittedDraft == nil {
			return admissionCapture{}, materialError("metadata", nil)
		}
		m.bgm, m.tmdb = req.submittedDraft.Metadata.Bgm, req.submittedDraft.Metadata.Tmdb
		m.mediaKindOverride = req.submittedDraft.Metadata.MediaKindOverride
		m.animeYear = req.submittedDraft.Metadata.Year
		m.animeSeason = req.submittedDraft.Metadata.Season
		m.config.business, err = req.submitted.Freeze()
		if err == nil {
			m.config.digest, err = m.config.business.Digest()
		}
	default:
		return admissionCapture{}, materialError("config", nil)
	}
	if err != nil {
		return admissionCapture{}, materialError("config", err)
	}
	if req.configKind == "owner" {
		if err := tx.QueryRowContext(ctx, `SELECT subgroups_version FROM anime_business_configs WHERE managed_anime_id=$1`, mg.managedID).Scan(&m.subgroupsOwnerVersion); err != nil {
			return admissionCapture{}, acceptError(500, "INTERNAL_INVARIANT", "subgroups_owner_required", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT rules_version FROM anime_business_configs WHERE managed_anime_id=$1`, mg.managedID).Scan(&m.rulesOwnerVersion); err != nil {
			return admissionCapture{}, acceptError(500, "INTERNAL_INVARIANT", "rules_owner_required", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT parser_kind, parser_version FROM anime_business_configs WHERE managed_anime_id=$1 AND parser_version IS NOT NULL`, mg.managedID).Scan(&m.parserKind, &m.parserVersion); err != nil {
			return admissionCapture{}, materialError("parser", err)
		}
		var bgm, tmdb, kind, override sql.NullString
		var season, year, ownerSeason sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT bgm_work_id,tmdb_work_id,tmdb_media_kind,tmdb_season,media_kind_override,year,season,version FROM anime_metadata WHERE managed_anime_id=$1`, mg.managedID).Scan(&bgm, &tmdb, &kind, &season, &override, &year, &ownerSeason, &m.metadataVersion); err != nil {
			return admissionCapture{}, materialError("metadata", err)
		}
		m.animeYear = nil
		if year.Valid {
			value := int(year.Int64)
			m.animeYear = &value
		}
		m.animeSeason = nil
		if ownerSeason.Valid {
			value := int(ownerSeason.Int64)
			m.animeSeason = &value
		}
		m.bgm, m.tmdb, err = metadatabinding.BindingsFromColumns(bgm, tmdb, kind, season, mg.managedID)
		if err != nil {
			return admissionCapture{}, materialError("metadata", err)
		}
		m.mediaKindOverride = nullableMetadataOverride(override)
	}
	if m.managementNone {
		if err := captureMountMetadataTx(ctx, tx, &m); err != nil {
			return admissionCapture{}, err
		}
	}
	if err := captureWorkTitlesTx(ctx, tx, &m); err != nil {
		return admissionCapture{}, materialError("metadata", err)
	}
	ref := m.config.business.Programs.AttrRules
	m.programs.attr, err = attrrules.LoadExact(ctx, ref.Version, ref.Digest, func(ctx context.Context, fp string) ([]byte, error) {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT content FROM attr_rule_versions WHERE fingerprint=$1`, fp).Scan(&raw)
		return raw, err
	})
	if err != nil {
		return admissionCapture{}, materialError("programs", err)
	}
	// Load and validate the exact Vars source from this same transaction.
	if err := tx.QueryRowContext(ctx, `SELECT content #>> '{}' FROM naming_vars_versions WHERE fingerprint=$1`, m.config.business.Programs.Vars.Digest).Scan(&m.programs.varsSource); err != nil {
		return admissionCapture{}, materialError("programs", err)
	}
	m.programs.vars, err = namingvars.NewProgram(m.config.business.Programs.Vars, m.programs.varsSource)
	if err != nil {
		return admissionCapture{}, materialError("programs", err)
	}
	if req.catalog != nil {
		c := req.catalog
		if c.observationID == "" || c.feedID == "" || c.seedKey == "" {
			return admissionCapture{}, materialError("catalog", nil)
		}
		material := &catalogMaterial{seedKey: c.seedKey}
		material.observation, err = catalogreader.ReadGroupObservationTx(ctx, tx, c.observationID)
		if err != nil {
			return admissionCapture{}, materialError("observation", err)
		}
		material.feed, err = catalogdb.NewRepository(nil).ReadProviderFeedSnapshotTx(ctx, tx, c.feedID)
		if err != nil {
			return admissionCapture{}, materialError("feed", err)
		}
		m.catalog = material
	}
	// Direct release payload is its existing immutable input, not a synthetic
	// provider snapshot or seed claim. Missing input never becomes an empty digest.
	if req.catalog == nil {
		m.direct, err = captureDirectTx(ctx, tx, req.releaseID)
		if err != nil {
			return admissionCapture{}, err
		}
		m.payloadDigest = *m.direct.seed.PayloadDigest
		// The identity bytes and their guarding version come from one statement
		// snapshot; retain fail-closed behavior if the earlier source read differs.
		if m.release.version != m.direct.releaseVersion {
			return admissionCapture{}, acceptError(409, "INPUT_STALE", "release_input_changed", nil)
		}
	}
	captured, err := constructQualification(m)
	if err != nil {
		return admissionCapture{}, err
	}
	return captured, nil
}
