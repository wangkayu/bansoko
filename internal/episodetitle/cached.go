package episodetitle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"
)

// SourceBGM names the Bangumi episode-list source. A Bangumi subject is the
// whole lookup scope, so cached BGM rows are keyed with season 0.
const SourceBGM Source = "bgm"

// PersistentCache is the persistent store of the provider episode titles the
// whitelisted naming renders query (migration 046). Naming used never to read
// it: the owner decree of 2026-10-22 says renders on that whitelist may query
// TMDB/BGM once per (source, work, season, episode), and this table is the
// freeze point.
//
//   - a `known` row (the provider's list carries a title) never expires;
//   - an `absent` row (the provider has no title, or the episode sits beyond
//     the list's end) is re-checked after the negative TTL;
//   - a transient failure — network error, rate limit, timeout — is not
//     cached: the next render retries.
type PersistentCache struct {
	// DB is the store handle. Nil means the cache is not wired: every lookup
	// surfaces a transient error, never a guessed answer (the composition root
	// always wires one).
	DB *sql.DB
	// Lookup performs the client calls with its own in-process list cache,
	// singleflight, per-source concurrency bounds and query timeout.
	Lookup *Lookup
	// Now and NegativeTTL; zero values take the defaults.
	Now         func() time.Time
	NegativeTTL time.Duration
}

// DefaultNegativeTTL is the negative cache window, measured in hours as a
// named constant, not a tuning env. A known title never expires, so a stale
// negative is the only thing the window can ever refresh.
const DefaultNegativeTTL = 6 * time.Hour

// maxTitleBytes is the title bound the store accepts, matching
// render_episode_title_valid (001): a cached value can always be frozen into a
// render snapshot. A provider title outside the bounds reads as no title.
const maxTitleBytes = 512

// maxEpisodeNumber bounds one provider episode num and the store's row space.
const maxEpisodeNumber = 10000

// cacheQuery is one validated (source, work, season, episode) lookup.
type cacheQuery struct {
	Source  Source
	WorkID  string
	Season  int
	Episode int
}

// Title is one lookup answer.
type Title struct {
	// Known marks a definite answer: the provider's list carries a title for
	// the episode. !Known (absent) is also definite — the provider has none,
	// or the episode sits beyond the list's end. A transient failure returns
	// an error and is never cached.
	Known bool
	Title string
}

// EpisodeTitle answers one (source, work, season, episode). A stored row
// answers on its own (`known` always; `absent` within the negative TTL); a
// refresh ignores absent rows (the redo/repair path) without refetching a
// known answer.
func (c *PersistentCache) EpisodeTitle(ctx context.Context, source Source, workID string, season, episode int, refresh bool) (Title, error) {
	if err := ctx.Err(); err != nil {
		return Title{}, transient(err)
	}
	q := cacheQuery{Source: source, WorkID: strings.TrimSpace(workID), Season: season, Episode: episode}
	if q.Source != SourceTMDB && q.Source != SourceBGM {
		return Title{}, transient(fmt.Errorf("episode title cache has no client for source %q", source))
	}
	if q.WorkID == "" || len(q.WorkID) > 128 || q.Season < 0 || q.ItemlessEpisode(episode) {
		return Title{}, transient(fmt.Errorf("episode title cache query is out of range for %s %q season %d episode %d", source, workID, season, episode))
	}
	if c.DB == nil || c.Lookup == nil {
		return Title{}, transient(fmt.Errorf("episode title cache for %s is not wired", source))
	}
	ttl := c.negativeTTL()
	if !refresh {
		if title, ok, err := c.read(ctx, q, ttl); err != nil || ok {
			return title, err
		}
	}
	list, err := c.fetchShared(ctx, q, refresh, false)
	if err != nil {
		return Title{}, err
	}
	// Callers that joined the flight read their own row back, so the table
	// stays the freeze point for them too; the flight has already persisted it.
	if err := c.store(ctx, q, list); err != nil {
		return Title{}, transient(err)
	}
	title, _, err := c.read(ctx, q, ttl)
	if err != nil {
		return Title{}, err
	}
	return title, nil
}

// RefreshWork re-pulls one whole season list bypassing every cache layer —
// the in-process list cache, negative rows AND known rows — and overwrites the
// stored rows, a known title included. It is the positive-cache bypass of the
// 刷新元数据 action (the binding card, owner decree 2026-10-22); the rename
// and alignment renders stay cache-first and have no caller.
func (c *PersistentCache) RefreshWork(ctx context.Context, source Source, workID string, season int) error {
	if err := ctx.Err(); err != nil {
		return transient(err)
	}
	q := cacheQuery{Source: source, WorkID: strings.TrimSpace(workID), Season: season}
	if q.Source != SourceTMDB && q.Source != SourceBGM {
		return transient(fmt.Errorf("episode title cache has no client for source %q", source))
	}
	if q.WorkID == "" || len(q.WorkID) > 128 || q.Season < 0 {
		return transient(fmt.Errorf("episode title cache refresh is out of range for %s %q season %d", source, workID, season))
	}
	if c.DB == nil || c.Lookup == nil {
		return transient(fmt.Errorf("episode title cache for %s is not wired", source))
	}
	_, err := c.fetchShared(ctx, q, true, true)
	return err
}

// ItemlessEpisode is a compile-time convenience placeholder retained for API
// shape; a real episode number is at least 1.
func (q cacheQuery) ItemlessEpisode(episode int) bool {
	return episode < 1 || episode > maxEpisodeNumber
}

// negativeTTL resolves the configured negative window.
func (c *PersistentCache) negativeTTL() time.Duration {
	if c.NegativeTTL > 0 {
		return c.NegativeTTL
	}
	return DefaultNegativeTTL
}

// read reports a usable stored answer (ok) with its value, or false when the
// row is expired (absent rows only), absent, or not there.
func (c *PersistentCache) read(ctx context.Context, q cacheQuery, ttl time.Duration) (Title, bool, error) {
	var state, title string
	var fetchedAt sql.NullTime
	err := c.DB.QueryRowContext(ctx, `
SELECT state, title, fetched_at FROM episode_title_cache
WHERE source=$1 AND work_id=$2 AND season=$3 AND episode=$4`,
		q.Source, q.WorkID, q.Season, q.Episode).Scan(&state, &title, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Title{}, false, nil
	}
	if err != nil {
		return Title{}, false, transient(err)
	}
	switch state {
	case "known":
		return Title{Known: true, Title: strings.TrimSpace(title)}, true, nil
	case "absent":
		// Only the definite negative expires; the fetch time is not null.
		age := c.now().Sub(fetchedAt.Time)
		return Title{}, age < ttl, nil
	default:
		return Title{}, false, transient(fmt.Errorf("episode title cache row %s %s season %d episode %d has state %q", q.Source, q.WorkID, q.Season, q.Episode, state))
	}
}

// fetchShared runs one season-list fetch per (source, work, season) flight,
// detached from caller cancellation like the plain reader. The bypass flag
// follows refresh, so a repair flight never reuses the in-process list cache.
// overwrite selects the positive-cache bypass store (the 刷新元数据 action):
// known rows are rewritten too.
func (c *PersistentCache) fetchShared(ctx context.Context, q cacheQuery, refresh, overwrite bool) (List, error) {
	flightKey := fmt.Sprintf("cache\x00%s\x00%s\x00%d", q.Source, q.WorkID, q.Season)
	if refresh {
		flightKey += "\x00bypass"
	}
	if overwrite {
		flightKey += "\x00overwrite"
	}
	detached := context.WithoutCancel(ctx)
	ch := group.DoChan(flightKey, func() (any, error) {
		list, err := c.Lookup.Season(detached, Key{Source: q.Source, WorkID: q.WorkID, Season: q.Season}, refresh)
		if err != nil {
			return List{}, err
		}
		persist := c.store
		if overwrite {
			persist = c.storeOverwrite
		}
		if persistErr := persist(detached, q, list); persistErr != nil {
			return List{}, persistErr
		}
		return list, nil
	})
	select {
	case <-ctx.Done():
		return List{}, transient(ctx.Err())
	case result := <-ch:
		if result.Err != nil {
			return List{}, result.Err
		}
		return result.Val.(List), nil
	}
}

// store upserts the titles the fetched list holds, plus the definite absent
// answer of the requested episode when the list does not name it. A `known`
// row is never rewritten: the positive answer is the freeze point, even if a
// refetched list would call it different or empty today.
func (c *PersistentCache) store(ctx context.Context, q cacheQuery, list List) error {
	return c.storeRows(ctx, q, list, false)
}

// storeOverwrite is the refresh store: every listed row is written over the
// stored answer, a known row included. A title the provider no longer names
// becomes a definite absent.
func (c *PersistentCache) storeOverwrite(ctx context.Context, q cacheQuery, list List) error {
	return c.storeRows(ctx, q, list, true)
}

func (c *PersistentCache) storeRows(ctx context.Context, q cacheQuery, list List, overwrite bool) error {
	type row struct {
		Episode int    `json:"episode"`
		State   string `json:"state"`
		Title   string `json:"title"`
	}
	rows := make([]row, 0, len(list.Episodes)+1)
	seen := map[int]bool{}
	add := func(episode int, title string) {
		if episode < 1 || episode > maxEpisodeNumber || seen[episode] {
			return
		}
		seen[episode] = true
		state, value := "absent", ""
		if title = normalizeTitle(title); title != "" {
			state, value = "known", title
		}
		rows = append(rows, row{Episode: episode, State: state, Title: value})
	}
	for _, item := range list.Episodes {
		add(item.Number, item.Title)
	}
	// A requested episode the list never named is a definite negative: the
	// store records it so the same request never repeats the fetch.
	if !seen[q.Episode] {
		add(q.Episode, "")
	}
	// Deterministic order keeps a retry's rows bit-for-bit comparable.
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].Episode < rows[i].Episode {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("serialize episode title cache rows: %w", err)
	}
	conflict := "WHERE episode_title_cache.state = 'absent'"
	if overwrite {
		conflict = ""
	}
	_, err = c.DB.ExecContext(ctx, `
INSERT INTO episode_title_cache (source, work_id, season, episode, state, title, fetched_at)
SELECT $1, $2, $3, record.episode, record.state, record.title, $4
FROM jsonb_to_recordset($5) AS record(episode INT, state TEXT, title TEXT)
ON CONFLICT (source, work_id, season, episode) DO UPDATE
    SET state = EXCLUDED.state, title = EXCLUDED.title, fetched_at = EXCLUDED.fetched_at, version = episode_title_cache.version + 1
  `+conflict,
		q.Source, q.WorkID, q.Season, c.now(), payload)
	if err != nil {
		return fmt.Errorf("store episode title cache rows: %w", err)
	}
	return nil
}

func (c *PersistentCache) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// normalizeTitle accepts a provider title the snapshot validator would too:
// trimmed, 1..512 bytes, without control characters. Anything else reads as no
// title.
func normalizeTitle(title string) string {
	value := strings.TrimSpace(title)
	if value == "" || len(value) > maxTitleBytes {
		return ""
	}
	if strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) }) {
		return ""
	}
	return value
}

// group is the process-wide singleflight of the persistent cache flights. The
// plain reader keeps its own group inside Lookup; the cache adds a layer above
// it so concurrent callers share one store write per season.
var group singleflight.Group
