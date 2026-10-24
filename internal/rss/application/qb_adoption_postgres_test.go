package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/acquisition/torrent"
	downloadadmission "bansoko/internal/download/admission"
	downloaddomain "bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type fixedDownloaderAdmission struct {
	result downloadadmission.Result
	err    error
	calls  []downloadadmission.Request
}

func (f *fixedDownloaderAdmission) MatchAdmission(_ context.Context, request downloadadmission.Request) (downloadadmission.Result, error) {
	f.calls = append(f.calls, request)
	return f.result, f.err
}

func TestOrdinaryRSSDownloaderAdmissionZeroUniqueAndAmbiguous(t *testing.T) {
	initTestNamingVars(t)
	for _, test := range []struct {
		name       string
		matcher    *fixedDownloaderAdmission
		wantTask   bool
		wantReason string
		wantAdopt  bool
		wantRetry  bool
	}{
		{name: "zero match submits normally", matcher: &fixedDownloaderAdmission{result: downloadadmission.Result{PathSnapshot: downloaddomain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}}}, wantTask: true, wantReason: "accepted"},
		{name: "unique match adopts external fact", matcher: &fixedDownloaderAdmission{result: downloadadmission.Result{PathSnapshot: downloaddomain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}, Provenance: &downloadadmission.Provenance{Kind: "unique_infohash_path", ProviderTaskID: "hash:old-owner", OperationKeyChecked: "qb:release"}}}, wantTask: true, wantReason: "accepted", wantAdopt: true},
		{name: "many matches fail closed", matcher: &fixedDownloaderAdmission{err: downloadadmission.ErrAmbiguousMatch}, wantReason: "qb_adoption_ambiguous"},
		{name: "conflicting match fails closed", matcher: &fixedDownloaderAdmission{err: downloadadmission.ErrConflictingMatch}, wantReason: "qb_adoption_conflict"},
		{name: "transient lookup stays retryable", matcher: &fixedDownloaderAdmission{err: errors.New("qB task lookup failed with HTTP 503")}, wantReason: "qb_admission_lookup_failed", wantRetry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openRSSDatabase(t)
			sourceID, _ := seedRSSOwner(t, db, false, true)
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte("d4:infod4:name11:show-01.mkv6:lengthi1eee")
			metadata, err := torrent.Parse(payload)
			if err != nil {
				t.Fatal(err)
			}
			size := metadata.Files[0].SizeBytes
			fetcher := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{
				GUID: "qb-adoption", Title: "[Group] Test Show - 01 1080p", Enclosure: "https://downloads.example.test/show-01.torrent",
				TorrentFilename: metadata.TorrentName, TorrentInfoHash: metadata.InfoHashV1, TorrentInfoHashV1: metadata.InfoHashV1,
				TorrentVersion: metadata.TorrentVersion, TorrentSizeBytes: metadata.TotalSize, TorrentPayload: payload,
				ReleaseFiles: []acquisitiondomain.ReleaseFile{{SourcePath: metadata.Files[0].Path, Filename: metadata.Files[0].Name, SizeBytes: &size, MediaKind: metadata.Files[0].MediaKind}},
			}}}
			service, err := newRSSService(t, db, store, fetcher)
			if err != nil {
				t.Fatal(err)
			}
			if test.matcher.result.Provenance != nil {
				test.matcher.result.Provenance.ProviderInfoHash = metadata.InfoHashV1
			}
			service.SetTargetAcquisition(missingTargetProbe{})
			service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
			service.SetDownloaderAdmission(test.matcher)

			refresh := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
			refreshOutcome, err := service.Handle(context.Background(), refresh)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishWithResult(context.Background(), refresh, workitems.StateSucceeded, refreshOutcome.OwnerResult, refreshOutcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(context.Background(), "qb-admission-worker", []string{workitems.KindReleaseEvaluate}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := service.Handle(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantRetry {
				if outcome.Kind != worker.OutcomeRetry || outcome.ReasonCode != test.wantReason || outcome.WriteOwner == nil {
					t.Fatalf("retry outcome = %+v", outcome)
				}
				if _, err := store.Retry(context.Background(), claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
					t.Fatal(err)
				}
				assertReleaseState(t, db, claim.Owner.ID, "accepted_pending_download", test.wantReason, false)
				return
			}
			if outcome.Kind != worker.OutcomeFinish {
				t.Fatalf("release evaluation outcome = %+v", outcome)
			}
			if err := store.FinishWithResult(context.Background(), claim, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			if len(test.matcher.calls) != 1 || test.matcher.calls[0].OperationKey != "qb:"+claim.Owner.ID || test.matcher.calls[0].InfoHashV1 != metadata.InfoHashV1 {
				t.Fatalf("admission calls = %+v", test.matcher.calls)
			}
			assertReleaseState(t, db, claim.Owner.ID, map[bool]string{true: "accepted_pending_download", false: "acquisition_failed"}[test.wantTask], test.wantReason, test.wantTask)
			if !test.wantTask {
				return
			}
			var frozen string
			if err := db.QueryRowContext(context.Background(), `SELECT frozen_input::text FROM download_tasks WHERE release_id = $1`, claim.Owner.ID).Scan(&frozen); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(frozen, `"path_snapshot"`) || strings.Contains(frozen, `"adoption_provenance"`) != test.wantAdopt {
				t.Fatalf("frozen input = %s, want adoption=%v", frozen, test.wantAdopt)
			}
		})
	}
}

func TestOrdinaryRSSDownloaderAdmissionConcurrentFinishCreatesOneOwnerChain(t *testing.T) {
	initTestNamingVars(t)
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("d4:infod4:name11:show-01.mkv6:lengthi1eee")
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	size := metadata.Files[0].SizeBytes
	fetcher := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{
		GUID: "qb-adoption-concurrent", Title: "[Group] Test Show - 01 1080p", Enclosure: "https://downloads.example.test/show-01.torrent",
		TorrentFilename: metadata.TorrentName, TorrentInfoHash: metadata.InfoHashV1, TorrentInfoHashV1: metadata.InfoHashV1,
		TorrentVersion: metadata.TorrentVersion, TorrentSizeBytes: metadata.TotalSize, TorrentPayload: payload,
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{SourcePath: metadata.Files[0].Path, Filename: metadata.Files[0].Name, SizeBytes: &size, MediaKind: metadata.Files[0].MediaKind}},
	}}}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	service.SetDownloaderAdmission(&fixedDownloaderAdmission{result: downloadadmission.Result{
		PathSnapshot: downloaddomain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"},
		Provenance:   &downloadadmission.Provenance{Kind: "unique_infohash_path", ProviderTaskID: "hash:" + metadata.InfoHashV1, ProviderInfoHash: metadata.InfoHashV1, OperationKeyChecked: "qb:release"},
	}})
	refresh := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	refreshOutcome, err := service.Handle(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(context.Background(), refresh, workitems.StateSucceeded, refreshOutcome.OwnerResult, refreshOutcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(context.Background(), "qb-admission-concurrent", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Handle(context.Background(), claim)
	if err != nil || first.Kind != worker.OutcomeFinish {
		t.Fatalf("first outcome=%+v err=%v", first, err)
	}
	second, err := service.Handle(context.Background(), claim)
	if err != nil || second.Kind != worker.OutcomeFinish {
		t.Fatalf("second outcome=%+v err=%v", second, err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, outcome := range []worker.Outcome{first, second} {
		group.Add(1)
		go func(outcome worker.Outcome) {
			defer group.Done()
			results <- store.FinishWithResult(context.Background(), claim, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner)
		}(outcome)
	}
	group.Wait()
	close(results)
	succeeded := 0
	for finishErr := range results {
		if finishErr == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent finishes succeeded=%d, want exactly one", succeeded)
	}
	assertReleaseState(t, db, claim.Owner.ID, "accepted_pending_download", "accepted", true)
}
