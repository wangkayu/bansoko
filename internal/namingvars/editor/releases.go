package editor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/postprocessing/naming"
	rssapplication "bansoko/internal/rss/application"
)

type releaseLoader struct{ db *sql.DB }

func newReleaseLoader(db *sql.DB) ReleaseEvidenceLoader {
	if db == nil {
		return nil
	}
	return releaseLoader{db: db}
}

// LoadReleaseEvidence assembles one persisted release's Vars evidence the way
// the admission and preview paths do: the stored feed item, the frozen
// episode of its parser selection, the anime binding and tags, and the exact
// file inventory. A release without a complete inventory is not evaluated,
// like acceptance.
func (l releaseLoader) LoadReleaseEvidence(ctx context.Context, releaseID string) (namingvars.ReleaseEvidence, string, error) {
	var rawFacts, sourceFacts, internalFacts, parsedFacts []byte
	var stableKey, animeID string
	err := l.db.QueryRowContext(ctx, `
SELECT r.raw_facts, r.source_facts, r.internal_facts, r.parsed_facts, r.stable_item_key, sub.managed_anime_id
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE r.id = $1`, releaseID).Scan(&rawFacts, &sourceFacts, &internalFacts, &parsedFacts, &stableKey, &animeID)
	if errors.Is(err, sql.ErrNoRows) {
		return namingvars.ReleaseEvidence{}, "", &UnavailableError{Reason: ReasonReleaseNotFound}
	}
	if err != nil {
		return namingvars.ReleaseEvidence{}, "", fmt.Errorf("read release for naming vars dry run: %w", err)
	}
	item, err := rssapplication.DecodeStoredFeedItem(rawFacts, internalFacts, sourceFacts, parsedFacts, stableKey)
	if err != nil {
		return namingvars.ReleaseEvidence{}, "", &UnavailableError{Reason: ReasonEvidenceUnreadable}
	}
	files := make([]namingvars.File, 0, len(item.ReleaseFiles))
	for _, file := range item.ReleaseFiles {
		if file.SizeBytes == nil {
			return namingvars.ReleaseEvidence{}, "", &UnavailableError{Reason: ReasonInventoryMissing}
		}
		files = append(files, namingvars.File{Name: path.Base(file.SourcePath), Path: file.SourcePath, Size: *file.SizeBytes})
	}
	if len(files) == 0 {
		return namingvars.ReleaseEvidence{}, "", &UnavailableError{Reason: ReasonInventoryMissing}
	}
	parser, err := rssapplication.ResolvePersistedReleaseParserEvidence(ctx, l.db, releaseID, item.Title)
	if err != nil {
		return namingvars.ReleaseEvidence{}, "", &UnavailableError{Reason: ReasonParserUnavailable}
	}
	evidence, err := varspersistence.ItemEvidence(ctx, l.db, animeID, releaseID, item, naming.VarsEpisode(parser.NamingFacts()))
	if err != nil {
		return namingvars.ReleaseEvidence{}, "", err
	}
	evidence.Files = files
	return evidence, animeID, nil
}

// ListReleases returns the most recently discovered releases with the anime
// they belong to.
func (l releaseLoader) ListReleases(ctx context.Context, limit int) ([]ReleaseOption, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT r.id, COALESCE(r.raw_facts->>'title', ''), COALESCE(a.title, '')
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime a ON a.id = sub.managed_anime_id
ORDER BY r.discovered_at DESC, r.id
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list releases for naming vars dry run: %w", err)
	}
	defer rows.Close()
	out := []ReleaseOption{}
	for rows.Next() {
		var option ReleaseOption
		if err := rows.Scan(&option.ID, &option.Title, &option.AnimeTitle); err != nil {
			return nil, err
		}
		if strings.TrimSpace(option.Title) == "" {
			continue
		}
		out = append(out, option)
	}
	return out, rows.Err()
}
