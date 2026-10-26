package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// fetchAniBTFixture runs the real AniBT fetcher against the recorded feed so
// the chain consumes exactly what production parsing produces.
func fetchAniBTFixture(t *testing.T) []acquisitiondomain.FeedItem {
	t.Helper()
	payload, err := os.ReadFile("../../acquisition/domain/testdata/anibt-rss-1.0.xml")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	items, err := (acquisitionapplication.HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "517057", "group-1")
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func drainReleaseEvaluations(t *testing.T, store *workitems.Store, service *Service) int {
	t.Helper()
	count := 0
	for {
		claim, err := store.ClaimDue(context.Background(), "tag-filter-worker", []string{workitems.KindReleaseEvaluate}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			return count
		}
		if err != nil {
			t.Fatal(err)
		}
		finishReleaseClaim(t, store, service, claim)
		count++
	}
}

type releaseVerdict struct {
	verdict, reason, detail string
}

func readReleaseVerdictByGUID(t *testing.T, db *sql.DB, sourceID, guid string) (string, releaseVerdict) {
	t.Helper()
	var id string
	var result releaseVerdict
	var detail sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT id, verdict, reason_code, reason_detail FROM releases
WHERE rss_source_id = $1 AND source_facts->>'identity_value' = $2`, sourceID, guid).Scan(&id, &result.verdict, &result.reason, &detail); err != nil {
		t.Fatalf("read release %s: %v", guid, err)
	}
	result.detail = detail.String
	return id, result
}

// One AniBT stub source through refresh and Release evaluation: the frozen
// AniBT tags drive the AniBT row for empty, whitelist and blacklist rules; a
// change of the subscription's own tag_filters re-evaluates unsubmitted
// releases only.
// tagRow builds a tag_filters row: "whitelist" tags are required, "blacklist"
// tags are excluded.
func tagRow(mode string, tags ...string) map[string]any {
	tags = append([]string{}, tags...)
	if mode == "blacklist" {
		return map[string]any{"require": []string{}, "exclude": tags}
	}
	return map[string]any{"require": tags, "exclude": []string{}}
}

func TestAniBTTagFilterRealChain(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	// Complete metadata: a metadata hold would mask every filter verdict.
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	if _, err := db.ExecContext(ctx, `
UPDATE rss_sources
SET source_kind = 'provider_group', provenance = '{"provider":"anibt","provider_entry_ref":"517057","provider_group_ref":"group-1","discovery_snapshot_version":"v1"}'::jsonb,
    provider = 'anibt', provider_entry_ref = '517057', provider_group_ref = 'group-1', discovery_snapshot_version = 'v1'
WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available,availability_generation) VALUES('anibt-tag-selection',$1,'anibt','group-1','Test Group','auto',TRUE,50,TRUE,1)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The recorded "[7³ACG] 【我推的孩子】/Oshi no Ko S01 | 25-35" title is
	// rejected by the activity-log private-path redaction (a pre-existing,
	// unrelated limitation), so the chain drives the other three releases.
	var items []acquisitiondomain.FeedItem
	for _, item := range fetchAniBTFixture(t) {
		if item.GUID != "rel_M_8_RXhEpRco" {
			items = append(items, item)
		}
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: items})
	if err != nil {
		t.Fatal(err)
	}
	refresh := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	outcome, err := service.Handle(ctx, refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := store.FinishWithResult(ctx, refresh, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	drainReleaseEvaluations(t, store, service)

	// The tags are frozen into the public source facts.
	var sourceFacts []byte
	if err := db.QueryRowContext(ctx, `SELECT source_facts FROM releases WHERE rss_source_id = $1 AND source_facts->>'identity_value' = 'rel_raw_none'`, sourceID).Scan(&sourceFacts); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		AniBT *StoredAniBTTags `json:"anibt"`
	}
	if err := json.Unmarshal(sourceFacts, &frozen); err != nil || frozen.AniBT == nil || frozen.AniBT.Schema != acquisitiondomain.AniBTTagSchemaV1 || len(frozen.AniBT.Tags) != 5 {
		t.Fatalf("frozen source facts = %s (%v)", sourceFacts, err)
	}

	const (
		full    = "rel_ELlQ5vDd4Aqu" // 1080p, CHS+CHT, INTERNAL, MKV, HEVC/10bit/WEBRip/AAC
		raw     = "rel_raw_none"     // 720p, NONE, MP4, AVC/WEB-DL, no language
		untaged = "rel_no_tags"      // current namespace, no tag fields
	)
	// A release that passes both filter rows continues to acquisition; the
	// fixture has no torrent payload, so "passed" means: not filtered.
	hold := releaseVerdict{verdict: "passed"}
	assertVerdicts := func(stage string, want map[string]releaseVerdict) {
		t.Helper()
		for guid, expected := range want {
			_, got := readReleaseVerdictByGUID(t, db, sourceID, guid)
			if expected.verdict == "passed" {
				if got.verdict == "filtered" || strings.HasPrefix(got.reason, "anibt_tag") {
					t.Fatalf("%s: release %s = %+v, want not filtered", stage, guid, got)
				}
				continue
			}
			if got.verdict != expected.verdict || got.reason != expected.reason || got.detail != expected.detail {
				t.Fatalf("%s: release %s = %+v, want %+v", stage, guid, got, expected)
			}
		}
	}
	// Empty rules filter nothing.
	assertVerdicts("empty rules", map[string]releaseVerdict{full: hold, raw: hold, untaged: hold})

	// A submitted release is frozen: it keeps its verdict and is never
	// rescheduled by a rules change.
	submittedID, _ := readReleaseVerdictByGUID(t, db, sourceID, full)
	owner := disposabledb.Owner(t)
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "tag-root", "tag-task", submittedID, "tag-task-key", `{}`, `{}`, "queued", `null`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'tag-task', verdict = 'submitted', reason_code = 'submitted', reason_detail = NULL WHERE id = $1`, submittedID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// storeTagFilters writes the subscription's own tag_filters value and
	// schedules re-evaluation of its unsubmitted releases, as an owner rules
	// save does.
	storeTagFilters := func(value map[string]any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `UPDATE anime_config_overrides SET value = $2, updated_at = NOW() WHERE managed_anime_id = $1 AND section_name = 'tag_filters'`, "anime-rss-test", raw); err != nil {
			t.Fatalf("store tag_filters: %v", err)
		}
		rows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1 AND r.download_task_id IS NULL
  AND r.verdict NOT IN ('adopted_existing', 'acquisition_failed', 'resume_baseline')`, "anime-rss-test")
		if err != nil {
			t.Fatal(err)
		}
		type target struct {
			id      string
			version int64
		}
		var targets []target
		for rows.Next() {
			var next target
			if err := rows.Scan(&next.id, &next.version); err != nil {
				t.Fatal(err)
			}
			targets = append(targets, next)
		}
		rows.Close()
		for _, next := range targets {
			if _, _, err := store.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
				Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: next.id}, Kind: workitems.KindReleaseEvaluate,
				IdempotencyKey: "release_evaluate:" + next.id, InputSnapshot: json.RawMessage(`{"trigger":"rules_patch"}`),
				MaxAttempts: 5, ExpectedOwnerVersion: next.version,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	put := func(_ int64, mode string, tags ...string) {
		t.Helper()
		storeTagFilters(map[string]any{"anibt": tagRow(mode, tags...), "title": tagRow("whitelist")})
	}
	countSubmittedWork := func() int {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate' AND state IN ('available','leased')`, submittedID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Whitelist: one tag per named dimension must be present; no tags = missing.
	put(0, "whitelist", "language:CHS", "resolution:1080p")
	if countSubmittedWork() != 0 {
		t.Fatal("a submitted release was rescheduled by a tag_filters change")
	}
	if drainReleaseEvaluations(t, store, service) == 0 {
		t.Fatal("tag_filters save scheduled no re-evaluation")
	}
	assertVerdicts("whitelist", map[string]releaseVerdict{
		raw:     {"filtered", "anibt_tag_whitelist_unmet", "missing: language:CHS, resolution:1080p"},
		untaged: {"filtered", "anibt_tag_missing", "missing: language:CHS, resolution:1080p"},
	})
	if _, got := readReleaseVerdictByGUID(t, db, sourceID, full); got.verdict != "submitted" {
		t.Fatalf("submitted release changed: %+v", got)
	}

	// Blacklist: any selected tag filters; untagged releases pass.
	put(1, "blacklist", "format:MP4", "tag:HEVC")
	drainReleaseEvaluations(t, store, service)
	assertVerdicts("blacklist", map[string]releaseVerdict{
		raw:     {"filtered", "anibt_tag_blacklisted", "blacklisted: format:MP4"},
		untaged: hold,
	})
	if _, got := readReleaseVerdictByGUID(t, db, sourceID, full); got.verdict != "submitted" {
		t.Fatalf("submitted release changed: %+v", got)
	}

	// Back to empty rules: nothing is tag-filtered again.
	put(2, "whitelist")
	drainReleaseEvaluations(t, store, service)
	assertVerdicts("empty again", map[string]releaseVerdict{raw: hold, untaged: hold})

	// The title-parse tags are frozen for every release with their parser
	// provenance.
	if err := db.QueryRowContext(ctx, `SELECT source_facts FROM releases WHERE rss_source_id = $1 AND source_facts->>'identity_value' = $2`, sourceID, raw).Scan(&sourceFacts); err != nil {
		t.Fatal(err)
	}
	var frozenTitle struct {
		Title *StoredTitleTags `json:"title_tags"`
	}
	if err := json.Unmarshal(sourceFacts, &frozenTitle); err != nil || frozenTitle.Title == nil || frozenTitle.Title.Implementation == "" || !strings.HasPrefix(frozenTitle.Title.ConfigDigest, "lexicon:sha256:") || !containsTag(frozenTitle.Title.Tags, "resolution:720p") || !containsTag(frozenTitle.Title.Tags, "source_type:WEB-DL") {
		t.Fatalf("frozen title tags = %s (%v)", sourceFacts, err)
	}

	// The title row applies to AniBT releases too, and both rows must pass;
	// the AniBT row is the recorded reason when both fail.
	putRows := func(_ int64, anibt, title map[string]any) {
		t.Helper()
		storeTagFilters(map[string]any{"anibt": anibt, "title": title})
	}
	row := tagRow
	putRows(3, row("whitelist"), row("whitelist", "resolution:1080p"))
	if drainReleaseEvaluations(t, store, service) == 0 {
		t.Fatal("title row save scheduled no re-evaluation")
	}
	assertVerdicts("title whitelist", map[string]releaseVerdict{
		raw:     {"filtered", "title_tag_whitelist_unmet", "missing: resolution:1080p"},
		untaged: hold,
	})
	putRows(4, row("whitelist"), row("blacklist", "source_type:WEB-DL"))
	drainReleaseEvaluations(t, store, service)
	assertVerdicts("title blacklist", map[string]releaseVerdict{
		raw:     {"filtered", "title_tag_blacklisted", "blacklisted: source_type:WEB-DL"},
		untaged: hold,
	})
	putRows(5, row("whitelist", "language:CHS"), row("blacklist", "source_type:WEB-DL"))
	drainReleaseEvaluations(t, store, service)
	assertVerdicts("both rows", map[string]releaseVerdict{
		raw:     {"filtered", "anibt_tag_whitelist_unmet", "missing: language:CHS"},
		untaged: {"filtered", "anibt_tag_missing", "missing: language:CHS"},
	})
	if _, got := readReleaseVerdictByGUID(t, db, sourceID, full); got.verdict != "submitted" {
		t.Fatalf("submitted release changed: %+v", got)
	}
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
