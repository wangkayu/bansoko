package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

// waitingReleaseFixture is one subscription with one selected AniBT group,
// its source, and one release of the source waiting for the competition.
func waitingReleaseFixture(t *testing.T) (*sql.DB, Dispatcher, providerdomain.GroupReadResult, []selectiondomain.GroupSelectionDecision) {
	t.Helper()
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	d := Dispatcher{DB: db, PolicyReader: unionPolicyReader{selectiondomain.DiscoveryPolicy{MaxRSS: 5, ProviderDefaults: map[providerdomain.Kind]selectiondomain.ProviderDefaultPolicy{providerdomain.ProviderAniBT: {Mode: selectiondomain.Ranked, Score: 50}}}}}
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "123")
	active := true
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Complete: true, CompletenessEvidence: "test-complete", Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "Name", Active: &active}}}
	decisions := []selectiondomain.GroupSelectionDecision{{Group: selectiondomain.EffectiveProviderGroup{Ref: ref, Mode: selectiondomain.Ranked, Score: 80}, Outcome: selectiondomain.GroupSelected}}
	if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, decisions, true); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title,
                         provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version, provenance)
VALUES ('source-1', 'sub-1', 'provider_group', 'https://feeds.example.test/123.xml', 'https://feeds.example.test/123.xml', 'Name', 'Test Anime',
        'anibt', '123', '123', 'v1',
        '{"provider":"anibt","provider_entry_ref":"123","provider_group_ref":"123","discovery_snapshot_version":"v1"}'::jsonb)`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts, verdict, reason_code, discovery_notice_due)
VALUES ('release-1', 'source-1', 'guid:release-1', '{"title":"placeholder"}'::jsonb, '{"identity_kind":"guid","identity_value":"release-1"}'::jsonb,
        '{"title":"[Name] Test Anime - 01"}'::jsonb, 'awaiting_competition', 'awaiting_competition', TRUE)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed waiting release: %v", err)
		}
	}
	return db, d, result, decisions
}

func requireWaitingRelease(t *testing.T, db *sql.DB, wantVerdict, wantReason string, wantNotice bool) {
	t.Helper()
	var verdict, reason string
	var notice bool
	if err := db.QueryRow(`SELECT verdict, reason_code, discovery_notice_due FROM releases WHERE id = 'release-1'`).Scan(&verdict, &reason, &notice); err != nil {
		t.Fatal(err)
	}
	if verdict != wantVerdict || reason != wantReason || notice != wantNotice {
		t.Fatalf("release = %s/%s notice=%v, want %s/%s notice=%v", verdict, reason, notice, wantVerdict, wantReason, wantNotice)
	}
}

// H1: the waiting releases of a group that leaves the selection end only when
// the transaction commits with the group still out. The authoritative
// discovery (every group of the provider unavailable, then restored), the
// discovery after a resume and the subgroups patch (synced selection, then
// recomputed) pass through intermediate states in one transaction and leave
// the release waiting; a committed deselection still drops it.
func TestSelectionIntermediateStatesKeepWaitingReleasesPostgres(t *testing.T) {
	ctx := context.Background()
	t.Run("authoritative discovery", func(t *testing.T) {
		db, d, result, decisions := waitingReleaseFixture(t)
		if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, decisions, true); err != nil {
			t.Fatal(err)
		}
		requireWaitingRelease(t, db, "awaiting_competition", "awaiting_competition", true)
	})
	t.Run("discovery after a resume", func(t *testing.T) {
		db, d, result, decisions := waitingReleaseFixture(t)
		if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE id = 'sub-1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = 'sub-1'`); err != nil {
			t.Fatal(err)
		}
		if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, decisions, true); err != nil {
			t.Fatal(err)
		}
		requireWaitingRelease(t, db, "awaiting_competition", "awaiting_competition", true)
	})
	t.Run("subgroups patch", func(t *testing.T) {
		db, d, _, _ := waitingReleaseFixture(t)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// The synced section deselects the group; the recompute that follows
		// in the same transaction selects it again.
		if _, err := tx.Exec(`UPDATE provider_group_selections SET effective_selected = FALSE, version = version + 1 WHERE subscription_id = 'sub-1'`); err != nil {
			t.Fatal(err)
		}
		if err := d.RecomputeSubscriptionSelections(ctx, txscope.Wrap(tx), "sub-1", "anime-1"); err != nil {
			t.Fatal(err)
		}
		var selected bool
		if err := tx.QueryRow(`SELECT available AND effective_selected FROM provider_group_selections WHERE subscription_id = 'sub-1' AND group_ref = '123'`).Scan(&selected); err != nil || !selected {
			t.Fatalf("the recompute must select the group again: %v %v", selected, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		requireWaitingRelease(t, db, "awaiting_competition", "awaiting_competition", true)
	})
	t.Run("committed deselection", func(t *testing.T) {
		db, _, _, _ := waitingReleaseFixture(t)
		if _, err := db.Exec(`UPDATE provider_group_selections SET effective_selected = FALSE, version = version + 1 WHERE subscription_id = 'sub-1'`); err != nil {
			t.Fatal(err)
		}
		requireWaitingRelease(t, db, "filtered", "provider_group_not_selected", false)
	})
	t.Run("committed unavailability", func(t *testing.T) {
		db, d, result, _ := waitingReleaseFixture(t)
		inactive := false
		result.Groups[0].Active = &inactive
		decisions := []selectiondomain.GroupSelectionDecision{{Group: selectiondomain.EffectiveProviderGroup{Ref: result.Groups[0].Ref, Mode: selectiondomain.Ranked, Score: 80}, Outcome: selectiondomain.GroupSelected}}
		if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, decisions, true); err != nil {
			t.Fatal(err)
		}
		requireWaitingRelease(t, db, "filtered", "provider_group_not_selected", false)
	})
}
