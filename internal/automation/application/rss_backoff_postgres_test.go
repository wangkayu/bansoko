package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// TestDispatchRSSRefreshHonoursBackoffAndProviderCircuit proves automatic RSS
// dispatch skips a source inside its failure backoff and every provider-group
// source of a paused provider, and dispatches again once either expires.
func TestDispatchRSSRefreshHonoursBackoffAndProviderCircuit(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-backoff', 'Backoff')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-backoff', 'anime-backoff', 'custom', 'Backoff', 'active')`)
	exec(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-custom', 'sub-backoff', 'custom_rss', 'https://example.test/custom', 'https://example.test/custom', 'Group', 'Backoff')`)
	exec(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version)
VALUES ('source-provider', 'sub-backoff', 'provider_group', 'https://example.test/provider', 'https://example.test/provider', 'Group', 'Backoff', 'anibt', '123', 'alpha', 'v1')`)
	exec(`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-custom'), ('source-provider')`)
	exec(`INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name, selection_mode, manual_selected, effective_selected, effective_score)
VALUES ('selection-backoff', 'sub-backoff', 'anibt', 'alpha', 'Group', 'auto', NULL, TRUE, 50)`)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The runtime role cannot delete WorkItems; test cleanup uses the owner.
	schemaOwner := disposabledb.Owner(t)
	dispatch := func() dispatchCounts {
		t.Helper()
		if _, err := schemaOwner.ExecContext(ctx, `WITH o AS (DELETE FROM work_item_outcomes WHERE kind = 'rss_refresh') DELETE FROM work_items WHERE kind = 'rss_refresh'`); err != nil {
			t.Fatal(err)
		}
		counts, err := (&Dispatcher{DB: db, WorkItems: store}).dispatchRSSRefresh(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}

	// Three failures: the backoff is 20 minutes from the last attempt.
	exec(`UPDATE rss_source_runtime SET consecutive_failures = 3, last_refresh_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes' WHERE rss_source_id = 'source-custom'`)
	exec(`INSERT INTO rss_provider_circuits(provider, consecutive_failures, last_failure_at, paused_until) VALUES ('anibt', 5, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 hour')`)
	if counts := dispatch(); counts.enqueued != 0 || counts.skipped != 2 {
		t.Fatalf("backoff/paused dispatch counts=%+v, want both skipped", counts)
	}

	exec(`UPDATE rss_source_runtime SET last_refresh_at = CURRENT_TIMESTAMP - INTERVAL '21 minutes' WHERE rss_source_id = 'source-custom'`)
	exec(`UPDATE rss_provider_circuits SET paused_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE provider = 'anibt'`)
	if counts := dispatch(); counts.enqueued != 2 || counts.skipped != 0 {
		t.Fatalf("expired backoff/pause dispatch counts=%+v, want both enqueued", counts)
	}
}

// TestDispatchRSSRefreshSkipsInjectedOnlySources pins the placeholder-source
// rule after the index-served rewrite (PRUNE-046): a source whose only
// releases are manually injected is skipped; a source with no releases, or
// with any non-injected release, is dispatched.
func TestDispatchRSSRefreshSkipsInjectedOnlySources(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	schemaOwner := disposabledb.Owner(t)
	exec := func(target interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}, query string, args ...any) {
		t.Helper()
		if _, err := target.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(db, `INSERT INTO managed_anime(id, title) VALUES ('anime-inj', 'Injected')`)
	exec(db, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-inj', 'anime-inj', 'custom', 'Injected', 'active')`)
	for _, id := range []string{"source-empty", "source-injected", "source-mixed"} {
		exec(db, `INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, 'sub-inj', 'custom_rss', 'https://example.test/'||$1, 'https://example.test/'||$1, 'Group', 'Injected')`, id)
		exec(db, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, id)
	}
	release := func(id, source, reason string) {
		exec(schemaOwner, `INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ($1, $2, $1, '{}', '{}', '{}', 'filtered', $3)`, id, source, reason)
	}
	release("rel-inj-1", "source-injected", "injected")
	release("rel-inj-2", "source-injected", "injected")
	release("rel-mix-1", "source-mixed", "injected")
	release("rel-mix-2", "source-mixed", "accepted")
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := (&Dispatcher{DB: db, WorkItems: store}).dispatchRSSRefresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.enqueued != 2 {
		t.Fatalf("dispatch counts=%+v, want the empty and the mixed source enqueued", counts)
	}
	rows, err := schemaOwner.QueryContext(ctx, `SELECT owner_id FROM work_items WHERE kind = 'rss_refresh' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if len(got) != 2 || got[0] != "source-empty" || got[1] != "source-mixed" {
		t.Fatalf("dispatched sources=%v, want [source-empty source-mixed]", got)
	}
}
