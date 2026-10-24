package filemaintenance

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/qualityprofile"
)

// compareVocabularyReleaseLimit bounds the releases scanned for seen AniBT
// values (the most recently discovered first).
const compareVocabularyReleaseLimit = 2000

// anibtCompareKeys maps an AniBT tag dimension to its comparison item.
var anibtCompareKeys = map[string]string{
	acquisitiondomain.AniBTDimensionResolution: qualityprofile.KeyAniBTResolution,
	acquisitiondomain.AniBTDimensionLanguage:   qualityprofile.KeyAniBTLanguage,
	acquisitiondomain.AniBTDimensionSubtitle:   qualityprofile.KeyAniBTSubtitle,
	acquisitiondomain.AniBTDimensionFormat:     qualityprofile.KeyAniBTFormat,
}

// CompareVocabularyApplication returns candidate values of the release.* and
// anibt.* comparison items: release.* from the active attribute rules (the
// title parse vocabulary), anibt.* from the raw spellings seen on releases of
// the anime (all releases when animeID is empty). The values are suggestions;
// the editor accepts any value. rt.* comes from the reltitle dictionary at the
// transport.
func (s *Service) CompareVocabularyApplication(ctx context.Context, animeID string) (map[string][]string, error) {
	out := map[string][]string{}
	if rules, _, err := attrrules.Current(); err == nil {
		out[qualityprofile.KeyReleaseLang] = append([]string{}, rules.Language...)
		out[qualityprofile.KeyReleaseResolution] = append([]string{}, rules.Resolution...)
	}
	if s == nil || s.retention == nil || s.retention.db == nil {
		return out, nil
	}
	seen, err := seenAniBTValues(ctx, s.retention.db, strings.TrimSpace(animeID))
	if err != nil {
		return nil, err
	}
	for key, values := range seen {
		out[key] = values
	}
	return out, nil
}

func seenAniBTValues(ctx context.Context, db *sql.DB, animeID string) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT tag.value
FROM (
    SELECT r.source_facts
    FROM releases r
    JOIN rss_sources src ON src.id = r.rss_source_id
    JOIN subscriptions sub ON sub.id = src.subscription_id
    WHERE ($1 = '' OR sub.managed_anime_id = $1)
      AND jsonb_typeof(r.source_facts->'anibt'->'tags') = 'array'
    ORDER BY r.discovered_at DESC, r.id DESC
    LIMIT $2
) recent
CROSS JOIN LATERAL jsonb_array_elements_text(recent.source_facts->'anibt'->'tags') AS tag(value)`, animeID, compareVocabularyReleaseLimit)
	if err != nil {
		return nil, fmt.Errorf("read seen AniBT values: %w", err)
	}
	defer rows.Close()
	byKey := map[string]map[string]string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		dimension, value, ok := acquisitiondomain.SplitTag(tag)
		key, known := anibtCompareKeys[dimension]
		value = strings.TrimSpace(value)
		if !ok || !known || value == "" {
			continue
		}
		if byKey[key] == nil {
			byKey[key] = map[string]string{}
		}
		folded := strings.ToLower(value)
		if _, exists := byKey[key][folded]; !exists {
			byKey[key][folded] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for key, values := range byKey {
		list := make([]string, 0, len(values))
		for _, value := range values {
			list = append(list, value)
		}
		sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i]) < strings.ToLower(list[j]) })
		out[key] = list
	}
	return out, nil
}
