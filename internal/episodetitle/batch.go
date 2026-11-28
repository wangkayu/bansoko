package episodetitle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// WorkKey identifies the persistent cache's season scope. Bangumi uses 0.
type WorkKey struct {
	Source Source `json:"source"`
	WorkID string `json:"work_id"`
	Season int    `json:"season"`
}

func (k WorkKey) Valid() bool {
	return (k.Source == SourceBGM || k.Source == SourceTMDB) && strings.TrimSpace(k.WorkID) != "" && len(k.WorkID) <= 128 && k.Season >= 0
}

type EpisodeKey struct {
	WorkKey
	Episode int `json:"episode"`
}

func (k EpisodeKey) Valid() bool {
	return k.WorkKey.Valid() && k.Episode > 0 && k.Episode <= maxEpisodeNumber
}

const ReasonCacheMiss = "cache_miss"

type DetailedTitle struct {
	Title
	// An empty Reason means the answer is definite (known or absent).
	Reason     string
	RetryAfter time.Duration
}

// EpisodeTitles answers the whole batch from PostgreSQL first. Each season
// with misses gets one list lookup and one bulk write. Provider failures apply
// only to those misses. Infrastructure errors fail the entire request.
func (c *PersistentCache) EpisodeTitles(ctx context.Context, work map[WorkKey][]int, cacheOnly map[WorkKey]bool) (map[EpisodeKey]DetailedTitle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var requested []EpisodeKey
	for key, episodes := range work {
		seen := make(map[int]bool, len(episodes))
		for _, episode := range episodes {
			item := EpisodeKey{WorkKey: key, Episode: episode}
			if !item.Valid() {
				return nil, fmt.Errorf("invalid episode title key: %+v", item)
			}
			if seen[episode] {
				continue
			}
			seen[episode] = true
			requested = append(requested, item)
		}
	}
	answers := make(map[EpisodeKey]DetailedTitle, len(requested))
	if len(requested) == 0 {
		return answers, nil
	}
	if c == nil || c.DB == nil {
		return nil, errors.New("episode title cache database is not wired")
	}
	if err := c.readBatch(ctx, requested, answers); err != nil {
		return nil, err
	}
	misses := map[WorkKey][]int{}
	for _, item := range requested {
		if _, ok := answers[item]; ok {
			continue
		}
		if cacheOnly[item.WorkKey] {
			answers[item] = DetailedTitle{Reason: ReasonCacheMiss}
			continue
		}
		misses[item.WorkKey] = append(misses[item.WorkKey], item.Episode)
	}

	type seasonResult struct {
		key      WorkKey
		episodes []int
		failure  *LookupError
	}
	results := make([]seasonResult, 0, len(misses))
	for key, episodes := range misses {
		results = append(results, seasonResult{key: key, episodes: episodes})
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i].key, results[j].key
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.WorkID != b.WorkID {
			return a.WorkID < b.WorkID
		}
		return a.Season < b.Season
	})
	group, runCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for i := range results {
		result := &results[i]
		group.Go(func() error {
			key := result.key
			if c.Lookup == nil {
				result.failure = &LookupError{Source: key.Source, Kind: LookupUnconfigured}
				return nil
			}
			list, err := c.Lookup.Season(runCtx, Key{Source: key.Source, WorkID: key.WorkID, Season: key.Season}, false)
			if err != nil {
				if runCtx.Err() != nil {
					return runCtx.Err()
				}
				if errors.As(err, &result.failure) {
					return nil
				}
				return err
			}
			return c.storeRows(runCtx, cacheQuery{Source: key.Source, WorkID: key.WorkID, Season: key.Season}, list, false, result.episodes)
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	var stored []EpisodeKey
	for _, result := range results {
		for _, episode := range result.episodes {
			item := EpisodeKey{WorkKey: result.key, Episode: episode}
			if result.failure == nil {
				stored = append(stored, item)
			} else {
				answers[item] = DetailedTitle{Reason: string(result.failure.Kind), RetryAfter: result.failure.RetryAfter}
			}
		}
	}
	if len(stored) > 0 {
		if err := c.readBatch(ctx, stored, answers); err != nil {
			return nil, err
		}
		for _, item := range stored {
			if _, ok := answers[item]; !ok {
				return nil, fmt.Errorf("persisted episode title disappeared: %+v", item)
			}
		}
	}
	return answers, ctx.Err()
}

func (c *PersistentCache) readBatch(ctx context.Context, requested []EpisodeKey, answers map[EpisodeKey]DetailedTitle) error {
	payload, err := json.Marshal(requested)
	if err != nil {
		return err
	}
	rows, err := c.DB.QueryContext(ctx, `
SELECT c.source, c.work_id, c.season, c.episode, c.state, c.title
FROM episode_title_cache c
JOIN jsonb_to_recordset($1) AS k(source TEXT, work_id TEXT, season INT, episode INT)
USING (source, work_id, season, episode)
WHERE c.state = 'known' OR c.fetched_at > $2`, payload, c.now().Add(-c.negativeTTL()))
	if err != nil {
		return fmt.Errorf("read episode title cache batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item EpisodeKey
		var state, title string
		if err := rows.Scan(&item.Source, &item.WorkID, &item.Season, &item.Episode, &state, &title); err != nil {
			return fmt.Errorf("scan episode title cache batch: %w", err)
		}
		switch state {
		case "known":
			answers[item] = DetailedTitle{Title: Title{Known: true, Title: strings.TrimSpace(title)}}
		case "absent":
			answers[item] = DetailedTitle{}
		default:
			return fmt.Errorf("invalid episode title cache state %q", state)
		}
	}
	return rows.Err()
}
