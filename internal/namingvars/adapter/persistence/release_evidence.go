package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/templatefacts"
)

// Querier is the read side of a *sql.DB or *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LoadAnimeEvidence reads the Vars anime evidence the way acceptance freezes
// it for a subscription: the managed anime's own titles (base, override,
// provider), its owner-edited year, its current metadata binding and the bound
// works' recorded titles.
// A missing binding or an unrecorded work leaves those values blank (null to
// the program); nothing is derived.
func LoadAnimeEvidence(ctx context.Context, q Querier, animeID string) (namingvars.AnimeEvidence, error) {
	evidence := namingvars.AnimeEvidence{AnimeID: animeID}
	if q == nil {
		return evidence, fmt.Errorf("vars anime evidence: database is not wired")
	}
	var titleOverride, providerTitle sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT title,title_override,provider_title FROM managed_anime WHERE id=$1`, animeID).Scan(&evidence.AnimeTitle, &titleOverride, &providerTitle); err != nil {
		return evidence, fmt.Errorf("vars anime evidence: %w", err)
	}
	evidence.AnimeTitleOverride, evidence.AnimeProviderTitle = strings.TrimSpace(titleOverride.String), strings.TrimSpace(providerTitle.String)
	var bgm, tmdb, kind sql.NullString
	var season, year, ownerSeason sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT bgm_work_id,tmdb_work_id,tmdb_media_kind,tmdb_season,year,season FROM anime_metadata WHERE managed_anime_id=$1`, animeID).Scan(&bgm, &tmdb, &kind, &season, &year, &ownerSeason)
	if errors.Is(err, sql.ErrNoRows) {
		return evidence, nil
	}
	if err != nil {
		return evidence, fmt.Errorf("vars anime evidence: %w", err)
	}
	if year.Valid {
		value := year.Int64
		evidence.AnimeYear = &value
	}
	if ownerSeason.Valid {
		value := ownerSeason.Int64
		evidence.AnimeSeason = &value
	}
	if bgm.Valid && strings.TrimSpace(bgm.String) != "" {
		evidence.BgmID = bgm.String
	}
	if tmdb.Valid && kind.Valid && season.Valid && strings.TrimSpace(tmdb.String) != "" {
		evidence.TmdbID, evidence.TmdbKind = tmdb.String, kind.String
		value := season.Int64
		evidence.TmdbSeason = &value
	}
	return evidence, FillWorkTitles(ctx, q, &evidence)
}

// FillWorkTitles reads the recorded native titles and provider years of the
// works the evidence binds (BgmID, TmdbID with TmdbKind). An unrecorded work
// keeps blank titles and a nil year; nothing is derived.
func FillWorkTitles(ctx context.Context, q Querier, evidence *namingvars.AnimeEvidence) error {
	var err error
	evidence.BgmTitle, evidence.BgmTitleCN, evidence.TmdbTitleCN, evidence.TmdbOriginalTitle = "", "", "", ""
	evidence.BgmYear, evidence.TmdbYear = nil, nil
	if q == nil {
		return nil
	}
	if strings.TrimSpace(evidence.BgmID) != "" {
		if evidence.BgmTitle, evidence.BgmTitleCN, _, evidence.BgmYear, err = workTitles(ctx, q, "bgm", "subject", evidence.BgmID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(evidence.TmdbID) != "" && strings.TrimSpace(evidence.TmdbKind) != "" {
		// A tmdb row records its titles in title_cn (migration 007); the first
		// return is the row's title, which a tmdb row no longer carries.
		if _, evidence.TmdbTitleCN, evidence.TmdbOriginalTitle, evidence.TmdbYear, err = workTitles(ctx, q, "tmdb", evidence.TmdbKind, evidence.TmdbID); err != nil {
			return err
		}
	}
	return nil
}

func workTitles(ctx context.Context, q Querier, provider, kind, id string) (title, titleCN, original string, year *int64, err error) {
	var t, cn, o sql.NullString
	var y sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT title, title_cn, original_title, year FROM metadata_work_titles WHERE provider=$1 AND media_kind=$2 AND work_id=$3`, provider, kind, id).Scan(&t, &cn, &o, &y)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", nil, nil
	}
	if err != nil {
		return "", "", "", nil, fmt.Errorf("vars anime evidence: %w", err)
	}
	if y.Valid {
		value := y.Int64
		year = &value
	}
	return t.String, cn.String, o.String, year, nil
}

// LoadReleaseTags reads a persisted release's frozen rt / anibt value
// namespaces, the same source_facts acceptance freezes. found is false for a
// release that is not persisted; undecodable source facts read as empty.
func LoadReleaseTags(ctx context.Context, q Querier, releaseID string) (tags templatefacts.Facts, found bool, err error) {
	if q == nil || strings.TrimSpace(releaseID) == "" {
		return tags, false, nil
	}
	var raw []byte
	err = q.QueryRowContext(ctx, `SELECT source_facts FROM releases WHERE id=$1`, releaseID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return tags, false, nil
	}
	if err != nil {
		return tags, false, fmt.Errorf("vars release tags: %w", err)
	}
	tags, _ = templatefacts.FromSourceFacts(raw)
	return tags, true, nil
}

// ItemEvidence assembles the Vars evidence of one release item for a preview
// or backfill call site, loading what the item does not carry: the anime
// evidence (LoadAnimeEvidence) and, for a persisted release, its frozen tags.
// An item that is not a persisted release uses its own frozen tag sets. The
// caller adds the files.
func ItemEvidence(ctx context.Context, q Querier, animeID, releaseID string, item acquisitiondomain.FeedItem, episode *namingvars.EpisodeEvidence) (namingvars.ReleaseEvidence, error) {
	anime := namingvars.AnimeEvidence{AnimeID: animeID}
	// A site without a database (unit-test previews) reads only the item.
	if q != nil && strings.TrimSpace(animeID) != "" {
		var err error
		if anime, err = LoadAnimeEvidence(ctx, q, animeID); err != nil {
			return namingvars.ReleaseEvidence{}, err
		}
	}
	return ReleaseItemEvidence(ctx, q, anime, releaseID, item, episode)
}

// ReleaseItemEvidence is ItemEvidence over already loaded anime evidence (a
// list preview loads it once, a draft preview builds it from the draft).
func ReleaseItemEvidence(ctx context.Context, q Querier, anime namingvars.AnimeEvidence, releaseID string, item acquisitiondomain.FeedItem, episode *namingvars.EpisodeEvidence) (namingvars.ReleaseEvidence, error) {
	evidence := namingvars.ReleaseEvidence{
		AnimeEvidence: anime,
		ReleaseTitle:  item.Title, ReleaseID: strings.TrimSpace(releaseID), Episode: episode,
		HashV1: item.TorrentInfoHashV1, HashV2: item.TorrentInfoHashV2,
	}
	tags, found, err := LoadReleaseTags(ctx, q, evidence.ReleaseID)
	if err != nil {
		return evidence, err
	}
	if !found {
		tags = templatefacts.FromTagSets(item.TitleTags, item.AniBTTags)
	}
	evidence.Tags = tags
	// A persisted release keeps its info hashes with its validated torrent
	// input; an item that carries none reads them there.
	if found && q != nil && strings.TrimSpace(evidence.HashV1) == "" && strings.TrimSpace(evidence.HashV2) == "" {
		var v1, v2 string
		err := q.QueryRowContext(ctx, `SELECT COALESCE(info_hash_v1,''), COALESCE(info_hash_v2,'') FROM release_torrent_inputs WHERE release_id=$1`, evidence.ReleaseID).Scan(&v1, &v2)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return evidence, fmt.Errorf("vars release hashes: %w", err)
		}
		evidence.HashV1, evidence.HashV2 = v1, v2
	}
	return evidence, nil
}

// PersistedReleaseEvidence is ReleaseItemEvidence for a persisted release whose
// frozen source facts and torrent input hashes the caller already read together
// with the release row (a list of releases reads them in its one release query
// instead of two more queries per release). The result is the one
// ReleaseItemEvidence builds for that release.
func PersistedReleaseEvidence(anime namingvars.AnimeEvidence, releaseID string, item acquisitiondomain.FeedItem, episode *namingvars.EpisodeEvidence, sourceFacts []byte, inputHashV1, inputHashV2 string) namingvars.ReleaseEvidence {
	evidence := namingvars.ReleaseEvidence{
		AnimeEvidence: anime,
		ReleaseTitle:  item.Title, ReleaseID: strings.TrimSpace(releaseID), Episode: episode,
		HashV1: item.TorrentInfoHashV1, HashV2: item.TorrentInfoHashV2,
	}
	evidence.Tags, _ = templatefacts.FromSourceFacts(sourceFacts)
	if strings.TrimSpace(evidence.HashV1) == "" && strings.TrimSpace(evidence.HashV2) == "" {
		evidence.HashV1, evidence.HashV2 = inputHashV1, inputHashV2
	}
	return evidence
}
