package application

// The library rename render (fileoperation prepare) is one of the whitelisted
// provider episode title render points (owner decree 2026-10-22): a loaded
// video filename template that references bgm.ep_title / tmdb.ep_title is
// resolved from the persistent cache BEFORE the render runs — the filename is
// the render's output, never a rewritten copy — the states freeze into the
// render snapshot's episode_titles block, the lookup runs exactly once per
// (source, work, season, episode) and every lookup failure that is not a
// definite answer stays a plan-stage retry, never a blocked placeholder plan.
// A template that never references a fact is the no-query majority: one shared
// prepare path serves the subscription flow and the one-shot flow alike, and
// an undefined namespace (${tmdb.*} with no TMDB binding) is the naming
// refusal, never a query.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/episodetitle"
	"bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// countingEpisodeClient is the fake provider the naming facts Querier reads
// through the real PersistentCache: it counts every provider call, so no-query
// and cache-first properties are observable without stubbing the resolver.
type countingEpisodeClient struct {
	mu    sync.Mutex
	calls int
	list  episodetitle.List
	err   error
}

func (c *countingEpisodeClient) Episodes(ctx context.Context, key episodetitle.Key) (episodetitle.List, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return episodetitle.List{}, c.err
	}
	return c.list, nil
}

func (c *countingEpisodeClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingEpisodeClient) swap(list episodetitle.List, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list, c.err = list, err
}

// wireCountingEpisodeTitleQueries wires the service the way main.go does, with
// the disposable schema's episode_title_cache as the persistent store and the
// counting client behind both sources.
func wireCountingEpisodeTitleQueries(t *testing.T, db *sql.DB, client *countingEpisodeClient) *namingfacts.Querier {
	t.Helper()
	return &namingfacts.Querier{Cache: &episodetitle.PersistentCache{
		DB: db,
		Lookup: episodetitle.New(episodetitle.Options{
			Clients: map[episodetitle.Source]episodetitle.Client{
				episodetitle.SourceBGM:  client,
				episodetitle.SourceTMDB: client,
			},
		}),
	}}
}

// episodeTemplateName points a fixture policy's TV video filename template at
// the episode title fact.
func episodeTemplateName(policy map[string]any, template string) {
	sections := policy["effective_sections"].(map[string]any)
	rules := naming.DefaultNamingRules()
	rules.TV.VideoFilenameTemplate = template
	sections[animedomain.SectionNamingRules] = rules
}

func readRenameSnapshot(t *testing.T, db *sql.DB, artifactID string) json.RawMessage {
	t.Helper()
	var snapshot json.RawMessage
	if err := db.QueryRow(`SELECT frozen_render_snapshot FROM file_operations WHERE artifact_id=$1`, artifactID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// A video filename template that reads tmdb.ep_title queries the provider once
// through the persistent cache, the rendered filename carries the title, the
// frozen snapshot closes the exact 025 block, and the answer lands as a known
// row keyed (tmdb, bound work, bound season, episode 1).
func TestPrepareVideoRenameRendersReferencedEpisodeTitle(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		episodeTemplateName(p, "[${tmdb.ep_title}].${file.ext}")
	})
	defer db.Close()
	client := &countingEpisodeClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "Pilot"}}}}
	service := newFixtureService(t, db, fixture)
	service.SetEpisodeTitleQueries(wireCountingEpisodeTitleQueries(t, db, client))
	prepareFixture(t, db, service, fixture.artifactID)

	if calls := client.callCount(); calls != 1 {
		t.Fatalf("one referenced fact runs the lookup exactly once, got %d", calls)
	}
	var target string
	if err := db.QueryRow(`SELECT target_path FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "[Pilot].mkv" {
		t.Fatalf("planned target = %q, want the queried title rendered", target)
	}
	snapshot := readRenameSnapshot(t, db, fixture.artifactID)
	var block struct {
		EpisodeTitles struct {
			Episode string `json:"episode"`
			TMDB    struct {
				State  string  `json:"state"`
				Value  *string `json:"value"`
				Reason *string `json:"reason_code"`
			} `json:"tmdb"`
			BGM struct {
				State  string  `json:"state"`
				Reason *string `json:"reason_code"`
			} `json:"bgm"`
		} `json:"episode_titles"`
	}
	if err := json.Unmarshal(snapshot, &block); err != nil {
		t.Fatalf("snapshot %s: %v", snapshot, err)
	}
	titles := block.EpisodeTitles
	if titles.Episode != "1" || titles.TMDB.State != "known" || titles.TMDB.Value == nil || *titles.TMDB.Value != "Pilot" || titles.TMDB.Reason != nil {
		t.Fatalf("tmdb block: %s", snapshot)
	}
	if titles.BGM.State != "unavailable" || titles.BGM.Reason == nil || *titles.BGM.Reason != "not_requested" {
		t.Fatalf("bgm block: %s", snapshot)
	}
	var state, title string
	if err := db.QueryRow(`SELECT state, title FROM episode_title_cache WHERE source='tmdb' AND work_id='tmdb-1' AND season=0 AND episode=1`).Scan(&state, &title); err != nil {
		t.Fatalf("the answer must land in the persistent cache: %v", err)
	}
	if state != "known" || title != "Pilot" {
		t.Fatalf("cached row = %s/%s, want known/Pilot", state, title)
	}
}

// The rename render is strictly cache-first: after one answer is stored, the
// same resolution never touches the provider again, against the same templates
// the prepare used.
func TestPrepareVideoRenameRenderIsStrictlyCacheFirst(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		episodeTemplateName(p, "[${tmdb.ep_title}].${file.ext}")
	})
	defer db.Close()
	client := &countingEpisodeClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "Pilot"}}}}
	service := newFixtureService(t, db, fixture)
	service.SetEpisodeTitleQueries(wireCountingEpisodeTitleQueries(t, db, client))
	prepareFixture(t, db, service, fixture.artifactID)
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("first render: %d calls", calls)
	}
	client.swap(episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "Changed"}}}, nil)

	rules := naming.DefaultNamingRules()
	rules.TV.VideoFilenameTemplate = "[${tmdb.ep_title}].${file.ext}"
	compiled, err := naming.NewEngine().CompileRulesWithVars(rules, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := naming.NamingContext{}
	ctx.Anime.TMDB = naming.TMDBNamingFacts{WorkID: "tmdb-1", MediaKind: "tv", Season: 0, SeasonPresent: true, SeasonBound: true}
	ctx.Parsed.Episode.Display = "1"
	for round := 0; round < 2; round++ {
		resolution, err := service.resolveEpisodeTitles(context.Background(), videoTitleTemplates(compiled, false, false), ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !resolution.TMDB.Known() || resolution.TMDB.Value != "Pilot" {
			t.Fatalf("round %d answers %v, want the stored Pilot", round, resolution.TMDB)
		}
	}
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("the stored known row never expires, got %d provider calls", calls)
	}
}

// The fact-free majority: a prepare whose loaded templates never read an
// episode title fact runs entirely without the provider and freezes no
// episode_titles key into its snapshot.
func TestPrepareVideoRenameWithoutReferenceNeverQueries(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	client := &countingEpisodeClient{err: errors.New("the no-reference render must never see the provider")}
	service := newFixtureService(t, db, fixture)
	service.SetEpisodeTitleQueries(wireCountingEpisodeTitleQueries(t, db, client))
	prepareFixture(t, db, service, fixture.artifactID)

	if calls := client.callCount(); calls != 0 {
		t.Fatalf("unreferenced facts never query, got %d calls", calls)
	}
	var keyed, total int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE frozen_render_snapshot ? 'episode_titles'), count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&keyed, &total); err != nil {
		t.Fatal(err)
	}
	if keyed != 0 || total != 1 {
		t.Fatalf("snapshots with the block=%d of %d operations, want only a block-less video rename", keyed, total)
	}
}

// One-shot and subscription share the prepare path: the one-shot's rename
// render resolves a referenced fact the same way — cache-first over the same
// persistent rows, no provider call, same rendered filename.
func TestPrepareOneShotRenamesThroughTheSameEpisodeTitlePath(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		episodeTemplateName(p, "[${tmdb.ep_title}].${file.ext}")
	})
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO episode_title_cache(source, work_id, season, episode, state, title, fetched_at)
VALUES ('tmdb', 'tmdb-1', 0, 1, 'known', 'Cached One-Shot', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	client := &countingEpisodeClient{err: errors.New("the one-shot render must run cache-first over the stored row")}
	service := newFixtureService(t, db, fixture)
	service.SetEpisodeTitleQueries(wireCountingEpisodeTitleQueries(t, db, client))
	seedOneShotMarkerFixture(t, db, "episode-title-marker")
	ref := workitems.OneShotRef{MarkerID: "episode-title-marker", CorrelationID: "corr-episode-title-marker", AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
		t.Fatal(err)
	}
	if calls := client.callCount(); calls != 0 {
		t.Fatalf("the stored known row answers the one-shot render, got %d calls", calls)
	}
	var target string
	if err := db.QueryRow(`SELECT target_path FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "[Cached One-Shot].mkv" {
		t.Fatalf("one-shot planned target = %q", target)
	}
	snapshot := readRenameSnapshot(t, db, fixture.artifactID)
	var wrapper struct {
		EpisodeTitles struct {
			TMDB struct {
				State string  `json:"state"`
				Value *string `json:"value"`
			} `json:"tmdb"`
		} `json:"episode_titles"`
	}
	if err := json.Unmarshal(snapshot, &wrapper); err != nil {
		t.Fatal(err)
	}
	tmdb := wrapper.EpisodeTitles.TMDB
	if tmdb.State != "known" || tmdb.Value == nil || *tmdb.Value != "Cached One-Shot" {
		t.Fatalf("one-shot block: %s", snapshot)
	}
}

// ${tmdb.*} with no TMDB binding is the naming undefined refusal — the
// config-problem branch, not a query: the provider is never touched and the
// plan lands the blocked placeholder with the typed reason.
func TestPrepareUnboundEpisodeTitleRefersUndefinedAndNeverQueries(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		values := p["fixture_media_values"].(map[string]any)
		values["tmdb"] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
		episodeTemplateName(p, "[${tmdb.ep_title}].${file.ext}")
	})
	defer db.Close()
	client := &countingEpisodeClient{err: errors.New("an undefined namespace never queries")}
	service := newFixtureService(t, db, fixture)
	service.SetEpisodeTitleQueries(wireCountingEpisodeTitleQueries(t, db, client))
	prepareFixture(t, db, service, fixture.artifactID)

	if calls := client.callCount(); calls != 0 {
		t.Fatalf("the undefined refusal happens before the lookup, got %d calls", calls)
	}
	var operations int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("undefined naming planned %d operations", operations)
	}
	var reason string
	if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, fixture.artifactID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "naming_fact_undefined_tmdb_ep_title" {
		t.Fatalf("failed reason = %q", reason)
	}
}

// A transient lookup failure is never the blocked-placeholder failure path:
// the plan stage's retry machinery re-enters the same preparation, the plan
// stays queued, the work item returns available on its own attempt budget and
// no placeholder operation, and no terminal file result, is ever written.
func TestPrepareTransientEpisodeTitleLookupStaysRetryable(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		episodeTemplateName(p, "[${tmdb.ep_title}].${file.ext}")
	})
	defer db.Close()
	client := &countingEpisodeClient{err: errors.New("provider unreachable")}
	service := newFixtureService(t, db, fixture)
	service.SetEpisodeTitleQueries(wireCountingEpisodeTitleQueries(t, db, client))
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim := claimPlanWorkItem(t, db, "episode-title-plan-worker")
	outcome, err := service.PostProcessingPlanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeCommitted || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("outcome=%+v, want the typed retry commit", outcome)
	}
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("the failing lookup runs once, got %d", calls)
	}
	var workState string
	var attempt int
	if err := db.QueryRow(`SELECT state,attempt FROM work_item_history WHERE id=$1`, claim.ID).Scan(&workState, &attempt); err != nil {
		t.Fatal(err)
	}
	if workState != "available" || attempt != claim.Attempt {
		t.Fatalf("retry reset budget/state: %s/%d", workState, attempt)
	}
	var planState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM postprocessing_plans WHERE id=$1`, planID).Scan(&planState); err != nil {
		t.Fatal(err)
	}
	if planState != "queued" {
		t.Fatalf("plan state = %q, want queued (postprocessing_plan_retry)", planState)
	}
	var operations, failedResults int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, fixture.artifactID).Scan(&failedResults); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || failedResults != 0 {
		t.Fatalf("no blocked placeholder may exist for a transient lookup: ops=%d failed=%d", operations, failedResults)
	}
}
