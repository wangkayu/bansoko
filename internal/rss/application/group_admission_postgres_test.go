package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"context"
	"testing"
)

func TestRSSHandlerRetirementBetweenHandleAndFinishPreservesSubmittedHistory(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE rss_sources SET source_kind='provider_group',provider='anibt',provider_entry_ref='123',provider_group_ref='alpha',discovery_snapshot_version='v1' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available,availability_generation) VALUES('scope',$1,'anibt','alpha','Alpha','auto',TRUE,50,TRUE,1)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{GUID: "prior-guid", Title: "[Group] Test Show - 01 1080p", Enclosure: "https://downloads.example.test/prior.torrent"}}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	first := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	outcome, err := service.Handle(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, first, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var id string
	var version int64
	if err := db.QueryRow(`SELECT id,version FROM releases WHERE rss_source_id=$1`, sourceID).Scan(&id, &version); err != nil {
		t.Fatal(err)
	}
	seed, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Rollback()
	acquisitionfixture.SeedObservedDownload(t, seed, "history-root", "history-task", id, "history-task-key", `{}`, `{}`, "queued", `null`)
	if _, err := seed.Exec(`UPDATE releases SET verdict='submitted',download_task_id='history-task' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	var runtimeVersion int64
	if err := db.QueryRow(`SELECT version FROM rss_source_runtime WHERE rss_source_id=$1`, sourceID).Scan(&runtimeVersion); err != nil {
		t.Fatal(err)
	}
	second := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, runtimeVersion)
	fake.items = append(fake.items, acquisitiondomain.FeedItem{GUID: "new-guid", Title: "[Group] Test Show - 02 1080p", Enclosure: "https://downloads.example.test/new.torrent"})
	outcome, err = service.Handle(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Fatalf("handler did not fetch: %d", fake.calls)
	}
	// Forced discovery's fenced scope commit wins after fetch but before admission.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM subscriptions WHERE id=$1 FOR UPDATE`, subscriptionID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE provider_group_selections SET available=FALSE,availability_generation=2 WHERE id='scope'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, second, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var count int
	var verdict string
	var actual int64
	if err := db.QueryRow(`SELECT count(*) FROM releases WHERE rss_source_id=$1`, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retired admission created release: %d", count)
	}
	if err := db.QueryRow(`SELECT verdict,version FROM releases WHERE id=$1`, id).Scan(&verdict, &actual); err != nil {
		t.Fatal(err)
	}
	if verdict != "submitted" || actual != version {
		t.Fatalf("submitted history changed: %s/%d want submitted/%d", verdict, actual, version)
	}
	if err := db.QueryRow(`SELECT count(*) FROM rss_sources WHERE id=$1`, sourceID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source deleted: %d %v", count, err)
	}
}

func TestRSSReleaseHandlerRetirementBeforeAdmissionCreatesNoDownload(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE rss_sources SET source_kind='provider_group',provider='anibt',provider_entry_ref='123',provider_group_ref='alpha',discovery_snapshot_version='v1' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score) VALUES('scope',$1,'anibt','alpha','Alpha','auto',TRUE,50)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	insertReleaseForEvaluation(t, db, sourceID, "scope-release", "[Group] Test Show - 01 1080p", "https://downloads.example.test/01.torrent")
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "scope-release"}, workitems.KindReleaseEvaluate, 1)
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.WriteOwner == nil {
		t.Fatal("missing admission writer")
	}
	if _, err := db.Exec(`UPDATE provider_group_selections SET available=FALSE,availability_generation=2 WHERE id='scope'`); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM download_tasks WHERE release_id='scope-release'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("retired admission created %d downloads", count)
	}
	var version int64
	if err := db.QueryRow(`SELECT version FROM releases WHERE id='scope-release'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("retired release rewritten: %d", version)
	}
}
