package application

import (
	"context"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// decidedRefreshItem is the one feed item the two fixtures below publish.
func decidedRefreshItem(t *testing.T) acquisitiondomain.FeedItem {
	t.Helper()
	payload := []byte("d4:infod4:name11:show-01.mkv6:lengthi1eee")
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	fileSize := metadata.Files[0].SizeBytes
	return acquisitiondomain.FeedItem{
		GUID: "guid-decided", Title: "[Group] Test Show - 01 1080p", Enclosure: "https://downloads.example.test/show-01.torrent", TorrentURL: "https://downloads.example.test/show-01.torrent",
		TorrentFilename: metadata.TorrentName, TorrentInfoHash: metadata.InfoHashV1, TorrentInfoHashV1: metadata.InfoHashV1, TorrentVersion: metadata.TorrentVersion, TorrentSizeBytes: metadata.TotalSize, TorrentPayload: payload,
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{SourcePath: metadata.Files[0].Path, Filename: metadata.Files[0].Name, SizeBytes: &fileSize, MediaKind: metadata.Files[0].MediaKind}},
	}
}

// TestRefreshDoesNotTakeBackADecidedVerdict is the regression this change
// exists for: every RSS cycle used to write the feed's own pre-admission
// verdict over the verdict the evaluation stage decided, which made the next
// poll look like a change and re-ran the evaluation and the competition
// forever. A decided Release now keeps its verdict, its version and its work
// item across a poll that changed nothing.
func TestRefreshDoesNotTakeBackADecidedVerdict(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{decidedRefreshItem(t)}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(version int64) {
		t.Helper()
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, version)
		outcome, err := service.Handle(context.Background(), claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	}
	refresh(1)

	ctx := context.Background()
	var releaseID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	// The evaluation stage decides the release: the competition finds no
	// placeable episode for it.
	var decidedVersion int64
	if err := db.QueryRowContext(ctx, `
UPDATE releases
SET verdict = 'filtered', reason_code = 'no_placeable_files', version = version + 1
WHERE id = $1
RETURNING version`, releaseID).Scan(&decidedVersion); err != nil {
		t.Fatal(err)
	}

	refresh(2)

	var version int64
	var verdict, reasonCode, feedVerdict string
	if err := db.QueryRowContext(ctx, `
SELECT version, verdict, COALESCE(reason_code, ''), COALESCE(feed_verdict, '')
FROM releases WHERE id = $1`, releaseID).Scan(&version, &verdict, &reasonCode, &feedVerdict); err != nil {
		t.Fatal(err)
	}
	if version != decidedVersion {
		t.Fatalf("an unchanged poll advanced the release: version=%d want %d", version, decidedVersion)
	}
	if verdict != "filtered" || reasonCode != "no_placeable_files" {
		t.Fatalf("the refresh took back the decision: verdict=%q reason=%q", verdict, reasonCode)
	}
	if feedVerdict != "accepted_pending_download" {
		t.Fatalf("feed verdict=%q, want the refresh's own accepted_pending_download", feedVerdict)
	}
	var liveItems int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM work_items
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate' AND state IN ('available', 'leased')`, releaseID).Scan(&liveItems); err != nil {
		t.Fatal(err)
	}
	if liveItems != 1 {
		t.Fatalf("an unchanged poll left %d live evaluations, want the original one", liveItems)
	}

	// A real feed change still re-enqueues: the title is new, so the facts and
	// the frozen evaluation inputs both move.
	changed := decidedRefreshItem(t)
	changed.GUID = "guid-decided"
	changed.Title = "[Group] Test Show - 01v2 1080p"
	fake.items = []acquisitiondomain.FeedItem{changed}
	refresh(3)
	if err := db.QueryRowContext(ctx, `SELECT version FROM releases WHERE id = $1`, releaseID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version <= decidedVersion {
		t.Fatalf("a changed feed item did not advance the release: version=%d want > %d", version, decidedVersion)
	}
	var evaluationVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT expected_owner_version FROM work_items
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate' AND state IN ('available', 'leased')`, releaseID).Scan(&evaluationVersion); err != nil {
		t.Fatal(err)
	}
	if evaluationVersion != version {
		t.Fatalf("the re-enqueued evaluation targets version %d, want %d", evaluationVersion, version)
	}
}

// TestRefreshStillWritesAFeedFinalVerdict pins the other half: a verdict the
// feed alone can reach is still recorded on the Release, because nothing later
// in the chain can turn an unreadable torrent into anything else.
func TestRefreshStillWritesAFeedFinalVerdict(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{decidedRefreshItem(t)}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(version int64) {
		t.Helper()
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, version)
		outcome, err := service.Handle(context.Background(), claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	}
	refresh(1)

	ctx := context.Background()
	var releaseID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET verdict = 'filtered', reason_code = 'no_placeable_files' WHERE id = $1`, releaseID); err != nil {
		t.Fatal(err)
	}

	unreadable := decidedRefreshItem(t)
	unreadable.TorrentPayload = nil
	unreadable.ReleaseFiles = nil
	unreadable.TorrentInfoHash = ""
	unreadable.TorrentInfoHashV1 = ""
	unreadable.TorrentVersion = ""
	unreadable.TorrentFilename = ""
	unreadable.TorrentSizeBytes = 0
	unreadable.TorrentErrorCode = "torrent_metadata_unavailable"
	unreadable.TorrentErrorReason = "temporary metadata outage"
	fake.items = []acquisitiondomain.FeedItem{unreadable}
	refresh(2)

	var verdict, reasonCode, reasonDetail, feedVerdict string
	if err := db.QueryRowContext(ctx, `
SELECT verdict, COALESCE(reason_code, ''), COALESCE(reason_detail, ''), COALESCE(feed_verdict, '')
FROM releases WHERE id = $1`, releaseID).Scan(&verdict, &reasonCode, &reasonDetail, &feedVerdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "parse_failed" || reasonCode != "torrent_metadata_unavailable" || reasonDetail != "temporary metadata outage" {
		t.Fatalf("feed-final verdict not recorded: %q/%q/%q", verdict, reasonCode, reasonDetail)
	}
	if feedVerdict != "parse_failed" {
		t.Fatalf("feed verdict=%q, want parse_failed", feedVerdict)
	}
}

// TestReleaseProcessedDedupeKeyIgnoresTheRowVersion pins the second half of the
// fix: the entry is named by the decision it records, so deriving the same
// decision again cannot append a second row.
func TestReleaseProcessedDedupeKeyIgnoresTheRowVersion(t *testing.T) {
	key := releaseProcessedDedupeKey("release-1", "accepted_pending_download", "filtered", "no_placeable_files")
	want := "release_processed:release:release-1:accepted_pending_download>filtered:evaluator-v1:no_placeable_files"
	if key != want {
		t.Fatalf("dedupe key = %q, want %q", key, want)
	}
	if same := releaseProcessedDedupeKey("release-1", "accepted_pending_download", "filtered", "no_placeable_files"); same != key {
		t.Fatalf("the same decision derived twice produced a different key: %q vs %q", same, key)
	}
	if other := releaseProcessedDedupeKey("release-1", "filtered", "accepted_pending_download", "accepted"); other == key {
		t.Fatalf("a different transition reused the key %q", other)
	}
}
