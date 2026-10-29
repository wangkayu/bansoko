package application

import (
	animedomain "bansoko/internal/anime/domain"
	"context"
	"database/sql"
	"encoding/json"
)

type BusinessEditView struct {
	TargetManagedAnimeID  string                 `json:"target_managed_anime_id"`
	Draft                 animedomain.AnimeDraft `json:"draft"`
	ExpectedOwnerVersions BusinessOwnerVersions  `json:"expected_owner_versions"`
}

// Read a repeatable, instance-owned baseline. Missing frozen business fails
// closed rather than silently turning current Global into the instance value.
func (s *Service) GetAnimeBusinessEdit(ctx context.Context, animeID string) (BusinessEditView, error) {
	var result BusinessEditView
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	versions, err := lockBusinessOwners(ctx, tx, animeID)
	if err != nil {
		return result, err
	}
	var candidateID string
	var revision int64
	var raw []byte
	var digest string
	if err := tx.QueryRowContext(ctx, `SELECT candidate_id,revision,business_snapshot,business_digest FROM anime_business_configs WHERE managed_anime_id=$1 AND business_snapshot IS NOT NULL`, animeID).Scan(&candidateID, &revision, &raw, &digest); err != nil {
		return result, businessInputError("instance has no complete frozen business editing baseline")
	}
	saved, _, err := s.LoadCandidateRevisionTx(ctx, tx, candidateID, revision)
	if err != nil {
		return result, err
	}
	draft := saved.Draft
	// After subscription tag_filters is owned by the instance rules tab; the
	// revision's draft copy is the value at subscribe time, not the current
	// one. Business apply never writes it, so the edit baseline omits it.
	draft.Rules.Sections.TagFilters = nil
	var business animedomain.BusinessSnapshot
	if err := json.Unmarshal(raw, &business); err != nil {
		return result, err
	}
	draft.Business = &business
	draft.BusinessDigest = &digest
	// Source/metadata/title are instance facts, not the old revision's edit values.
	if err := tx.QueryRowContext(ctx, `SELECT m.title,m.title_override,sub.origin,sub.origin_entry_ref,sub.instance_label FROM managed_anime m JOIN subscriptions sub ON sub.managed_anime_id=m.id WHERE m.id=$1`, animeID).Scan(&draft.Title, &draft.TitleOverride, &draft.Origin, &draft.OriginEntryRef, &draft.InstanceLabel); err != nil {
		return result, err
	}
	// Application is prohibited from mutating these fields; baseline keeps their
	// stable source identities. Dedicated metadata/source editors remain separate.
	var bgm, tmdb, kind sql.NullString
	var season, year, ownerSeason sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT bgm_work_id,tmdb_work_id,tmdb_media_kind,tmdb_season,media_kind,year,season FROM anime_metadata WHERE managed_anime_id=$1`, animeID).Scan(&bgm, &tmdb, &kind, &season, &draft.Metadata.MediaKind, &year, &ownerSeason); err != nil {
		return result, err
	}
	draft.Metadata.Year = nil
	if year.Valid {
		value := int(year.Int64)
		draft.Metadata.Year = &value
	}
	draft.Metadata.Season = nil
	if ownerSeason.Valid {
		value := int(ownerSeason.Int64)
		draft.Metadata.Season = &value
	}
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	if bgm.Valid {
		draft.Metadata.Bgm = &animedomain.BGMBinding{WorkId: bgm.String}
	}
	if tmdb.Valid {
		draft.Metadata.Tmdb = &animedomain.TMDBBinding{WorkId: tmdb.String, MediaKind: animedomain.TMDBMediaKind(kind.String), Season: int(season.Int64)}
	}
	if err := tx.QueryRowContext(ctx, `SELECT total_episodes FROM anime_metadata WHERE managed_anime_id=$1`, animeID).Scan(&draft.Metadata.TotalEpisodes); err != nil {
		return result, err
	}
	sources, err := tx.QueryContext(ctx, `SELECT src.source_kind,src.locator,src.locator_display,src.group_name,src.title,src.episode_offset,src.anibt_aux_mode,src.http_feed_connection_ref,src.network_route_ref,src.provenance FROM rss_sources src JOIN subscriptions sub ON sub.id=src.subscription_id WHERE sub.managed_anime_id=$1 ORDER BY src.created_at,src.id`, animeID)
	if err != nil {
		return result, err
	}
	draft.RssSources = []animedomain.RSSSourceDraft{}
	for sources.Next() {
		var source animedomain.RSSSourceDraft
		var provenance []byte
		if err := sources.Scan(&source.SourceKind, &source.Locator, &source.LocatorDisplay, &source.GroupName, &source.Title, &source.EpisodeOffset, &source.AnibtAuxMode, &source.HttpFeedConnectionRef, &source.NetworkRouteRef, &provenance); err != nil {
			sources.Close()
			return result, err
		}
		// The stored display of a provider source can be its full feed URL;
		// the read view shows the origin only, as every source read does.
		if source.LocatorDisplay != nil {
			display := animedomain.SanitizeLocatorDisplay(*source.LocatorDisplay)
			source.LocatorDisplay = &display
		}
		if len(provenance) > 0 {
			if err := json.Unmarshal(provenance, &source.ProviderProvenance); err != nil {
				sources.Close()
				return result, err
			}
		}
		draft.RssSources = append(draft.RssSources, source)
	}
	err = sources.Err()
	sources.Close()
	if err != nil {
		return result, err
	}
	// The editable rules must represent the stored business.
	if err := json.Unmarshal(business.Sections[animedomain.SectionSubgroups], &draft.Subgroups); err != nil {
		return result, err
	}
	manualRows, err := tx.QueryContext(ctx, `SELECT sel.provider,sel.group_ref,sel.display_name,sel.manual_selected,sel.effective_selected,sel.effective_score FROM provider_group_selections sel JOIN subscriptions sub ON sub.id=sel.subscription_id WHERE sub.managed_anime_id=$1 AND sel.selection_mode='manual' ORDER BY sel.provider,sel.group_ref`, animeID)
	if err != nil {
		return result, err
	}
	for manualRows.Next() {
		var intent animedomain.SubgroupScoreEntry
		if err := manualRows.Scan(&intent.Provider, &intent.GroupRef, &intent.DisplayName, &intent.ManualSelected, &intent.EffectiveSelected, &intent.Score); err != nil {
			manualRows.Close()
			return result, err
		}
		intent.SelectionMode = animedomain.SelectionModeManual
		entries := draft.Subgroups.Entries[:0]
		for _, entry := range draft.Subgroups.Entries {
			if entry.Provider != intent.Provider || entry.GroupRef != intent.GroupRef {
				entries = append(entries, entry)
			}
		}
		draft.Subgroups.Entries = append(entries, intent)
	}
	err = manualRows.Err()
	manualRows.Close()
	if err != nil {
		return result, err
	}
	wrapped := func(raw json.RawMessage) json.RawMessage {
		value, _ := json.Marshal(struct {
			Mode  string          `json:"mode"`
			Value json.RawMessage `json:"value"`
		}{"replace", raw})
		return value
	}
	draft.Rules.Sections = animedomain.RulesSections{Parser: wrapped(business.Sections[animedomain.SectionParser]), ReleaseFilter: wrapped(business.Sections[animedomain.SectionReleaseRules]), FileFilter: wrapped(business.Sections[animedomain.SectionFileRules]), Naming: wrapped(business.Sections[animedomain.SectionNamingRules]), FileEffect: wrapped(business.Sections[animedomain.SectionFileEffect])}
	result = BusinessEditView{animeID, draft, versions}
	return result, tx.Commit()
}
