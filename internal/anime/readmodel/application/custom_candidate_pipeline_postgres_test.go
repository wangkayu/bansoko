package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	rootapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type customPipelineFetcher struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (f *customPipelineFetcher) FetchCustomSnapshot(context.Context, string) ([]acquisitiondomain.FeedItem, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	return []acquisitiondomain.FeedItem{{GUID: "custom-seed", Title: "Test Show - 01", TorrentURL: "https://example.test/one.torrent", TorrentErrorCode: "torrent_metadata_unavailable", TorrentErrorReason: "fixed bad torrent"}, {GUID: "custom-parse-failure", Title: "No episode identity", TorrentURL: "https://example.test/unparsed.torrent", TorrentErrorCode: "torrent_metadata_unavailable", TorrentErrorReason: "fixed bad torrent"}}, nil
}
func (f *customPipelineFetcher) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// Uses real candidate save/patch/revision services and production exact snapshot
// adapter. Missing production atomic adapters fail the test, never skip.
func TestPGCustomCandidateExactSnapshotPreviewAndAtomicCAS(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	view, services := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	input := readmodelCandidateInput(t, db, readmodelTestDraft(t))
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.marker = {value: "frozen-custom", title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.Business = &business
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.BusinessDigest = &digest
	fetcher := &customPipelineFetcher{}
	candidate, err := services.Draft.SaveAnimeConfigCandidate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.count() != 0 {
		t.Fatal("save fetched")
	}
	revision := *candidate.CurrentRevision
	var _ catalogapplication.CustomSnapshotRevisionReader = services.Draft
	var _ catalogapplication.CustomSnapshotAtomicRevisionReader = services.Draft
	repository := catalogpersistence.NewRepository(db)
	app := catalogapplication.CustomSnapshotApplication{Revisions: services.Draft, Fetcher: fetcher, Snapshots: repository}
	// Opening a saved revision without a snapshot must be read-only.
	if _, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: candidate.Id, Revision: revision}); err != nil {
		t.Fatal(err)
	}
	if fetcher.count() != 0 {
		t.Fatal("opening revision fetched")
	}
	result, err := app.Fetch(ctx, candidate.Id, revision, candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.count() != 1 {
		t.Fatal("explicit fetch not exactly once")
	}
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetFeedSnapshotStore(repository)
	view.seedPreviews = rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload)
	ref := PreviewFeedSnapshotRef{Provider: "custom", ProviderEntryRef: result.ProviderEntryRef, ProviderGroupRef: result.GroupRef, SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash}
	projected, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: candidate.Id, Revision: revision, Feed: []PreviewFeedSnapshotRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Groups) != 1 || len(projected.Groups[0].Items) != 2 {
		t.Fatalf("projection %+v", projected)
	}
	item := projected.Groups[0].Items[0]
	if item.SnapshotID != result.SnapshotID || item.Torrent.ErrorCode == "" {
		t.Fatalf("bad torrent not observable %+v", item)
	}
	// Both seeds' torrent fetches failed, and a failed fetch leaves the seed
	// unexpanded: the fetch's own code stays visible on the torrent state while
	// nothing parses the title, so the row is pending rather than a verdict.
	for _, row := range projected.Groups[0].Items {
		if row.Torrent.ErrorCode != "torrent_metadata_unavailable" || row.Evaluation.State != RSSListEvaluationPending {
			t.Fatalf("torrent fetch failure not projected for %q: %+v", row.Seed.Title, row)
		}
	}
	frozen, err := view.freezePreviewVars(ctx, business.Programs.Vars, acquisitiondomain.FeedItem{Title: "Test Show - 01"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != "ok" || frozen.Output["marker"]["value"] != "frozen-custom" || frozen.Output["marker"]["title"] != "Test Show - 01" {
		t.Fatalf("actual frozen Vars output %+v", frozen)
	}
	if _, err := frozenpolicy.SeedBusiness(ctx, db, `vars.marker = {value: "unrelated-new-source"};`); err != nil {
		t.Fatal(err)
	}
	replay, err := view.freezePreviewVars(ctx, business.Programs.Vars, acquisitiondomain.FeedItem{Title: "Test Show - 01"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay.Output, frozen.Output) {
		t.Fatalf("exact Vars replay changed %+v", replay)
	}
	saved, err := services.Draft.GetAnimeConfigCandidateRevision(ctx, candidate.Id, revision)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Business.Programs != business.Programs {
		t.Fatal("frozen Vars program substituted")
	}
	restarted, _ := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	restarted.seedPreviews = view.seedPreviews
	if _, err := restarted.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: candidate.Id, Revision: revision, Feed: []PreviewFeedSnapshotRef{ref}}); err != nil {
		t.Fatal(err)
	}
	other, err := services.Draft.SaveAnimeConfigCandidate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: other.Id, Revision: *other.CurrentRevision, Feed: []PreviewFeedSnapshotRef{ref}}); err == nil {
		t.Fatal("other candidate accepted persisted snapshot")
	}
	for _, mutate := range []func(*PreviewFeedSnapshotRef){func(r *PreviewFeedSnapshotRef) { r.SnapshotVersion++ }, func(r *PreviewFeedSnapshotRef) { r.SnapshotID = "nonexistent-exact-snapshot" }} {
		wrong := ref
		mutate(&wrong)
		if _, err := restarted.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: candidate.Id, Revision: revision, Feed: []PreviewFeedSnapshotRef{wrong}}); err == nil {
			t.Fatalf("wrong exact snapshot accepted %+v", wrong)
		}
	}
	wrong := ref
	wrong.ContentHash = "sha256:" + fmt.Sprintf("%064d", 0)
	if _, err := restarted.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: candidate.Id, Revision: revision, Feed: []PreviewFeedSnapshotRef{wrong}}); err == nil {
		t.Fatal("wrong persisted identity accepted")
	}
	if fetcher.count() != 1 {
		t.Fatal("preview/restart fetched")
	}
	barrier := &customPipelineFetcher{entered: make(chan struct{}), release: make(chan struct{})}
	app.Fetcher = barrier
	done := make(chan error, 1)
	go func() { _, err := app.Fetch(ctx, candidate.Id, revision, candidate.Version); done <- err }()
	select {
	case <-barrier.entered:
	case err := <-done:
		t.Fatalf("fetch failed before barrier: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("fetch barrier timed out")
	}
	input.Draft.InstanceLabel = animetest.StringPtr("changed")
	if _, err := services.Draft.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, draftapplication.CandidateDraftInput{Draft: input.Draft}); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	if err := <-done; err == nil {
		t.Fatal("stale fetch persisted")
	} else {
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Code != "CUSTOM_CANDIDATE_CHANGED" {
			t.Fatalf("wrong CAS rejection %v", err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM provider_feed_snapshots WHERE provider_entry_ref=$1`, result.ProviderEntryRef).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stale snapshot inserted: %d", n)
	}
}

// A subscribed preset is dormant, and the one-shot browse of a subscribed custom
// card fetches that preset's frozen feed: the fetch is admitted for a dormant
// (activated) candidate exactly as for a working one, writes nothing to the
// preset, and is refused once the preset is archived.
func TestPGCustomSnapshotFetchAdmitsDormantPreset(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	_, services := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	input := readmodelCandidateInput(t, db, readmodelTestDraft(t))
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.marker = {value: "dormant-custom", title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.Business = &business
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.BusinessDigest = &digest
	candidate, err := services.Draft.SaveAnimeConfigCandidate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	revision := *candidate.CurrentRevision
	if _, err := services.Draft.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "dormant-custom-fetch-0001", draftapplication.CandidateSubscribeInput{Revision: revision}); err != nil {
		t.Fatal(err)
	}
	readState := func() (int64, string) {
		t.Helper()
		var version int64
		var status string
		if err := db.QueryRowContext(ctx, `SELECT version, status FROM anime_config_candidates WHERE id=$1`, candidate.Id).Scan(&version, &status); err != nil {
			t.Fatal(err)
		}
		return version, status
	}
	dormant, status := readState()
	if status != "dormant" {
		t.Fatalf("activated preset status = %s, want dormant", status)
	}
	app := catalogapplication.CustomSnapshotApplication{Revisions: services.Draft, Fetcher: &customPipelineFetcher{}, Snapshots: catalogpersistence.NewRepository(db)}
	result, err := app.Fetch(ctx, candidate.Id, revision, dormant)
	if err != nil {
		t.Fatalf("dormant preset fetch: %v", err)
	}
	if result.Provider != "custom" || result.SnapshotID == "" || len(result.Seeds) != 2 {
		t.Fatalf("dormant preset snapshot = %+v", result)
	}
	if version, status := readState(); version != dormant || status != "dormant" {
		t.Fatalf("fetch changed the preset: version %d status %s", version, status)
	}
	var archived int64
	if err := db.QueryRowContext(ctx, `UPDATE anime_config_candidates SET status='archived', activated_managed_anime_id=NULL, version=version+1 WHERE id=$1 RETURNING version`, candidate.Id).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	_, err = app.Fetch(ctx, candidate.Id, revision, archived)
	var business2 *apperrors.Business
	if !errors.As(err, &business2) || business2.Code != "CUSTOM_CANDIDATE_CHANGED" {
		t.Fatalf("archived preset fetch = %v, want CUSTOM_CANDIDATE_CHANGED", err)
	}
}
