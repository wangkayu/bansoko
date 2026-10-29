package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// largeReleaseTorrent assembles a v1 multi-file torrent whose files all sit at
// the first level below root: 131 episodes, 96 subtitles and 392 other files
// (audio tracks, scans, notes), the shape of a large box set flattened into one
// directory.
func largeReleaseTorrent(t *testing.T, root string) ([]byte, torrent.Metadata) {
	t.Helper()
	const tag = "[Ma10p_1080p][x265_flac_aac]"
	var names []string
	var sizes []int64
	add := func(name string, size int64) {
		names = append(names, name)
		sizes = append(sizes, size)
	}
	for episode := 1; episode <= 131; episode++ {
		add(fmt.Sprintf("[Group] Test Show - %03d %s.mkv", episode, tag), int64(1<<30+episode))
		add(fmt.Sprintf("[Group] Test Show - %03d %s.flac", episode, tag), int64(1<<25+episode))
		if episode <= 96 {
			add(fmt.Sprintf("[Group] Test Show - %03d %s.chs.ass", episode, tag), int64(1<<16+episode))
		}
	}
	for page := 1; page <= 200; page++ {
		add(fmt.Sprintf("[Group] Test Show Booklet Scan Page %03d %s.png", page, tag), int64(1<<21+page))
	}
	for index := 1; index <= 61; index++ {
		add(fmt.Sprintf("[Group] Test Show Disc Notes %02d %s.txt", index, tag), int64(1<<12+index))
	}
	var builder strings.Builder
	builder.WriteString("d4:infod5:filesl")
	for index, name := range names {
		builder.WriteString("d6:lengthi" + strconv.FormatInt(sizes[index], 10) + "e4:pathl" + strconv.Itoa(len(name)) + ":" + name + "ee")
	}
	builder.WriteString("e4:name" + strconv.Itoa(len(root)) + ":" + root + "ee")
	payload := []byte(builder.String())
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatalf("parse large release torrent: %v", err)
	}
	if len(metadata.Files) != 619 {
		t.Fatalf("large release torrent projects %d files, want 619", len(metadata.Files))
	}
	return payload, metadata
}

// A subscription release of 619 first-level files is admitted through the
// ordinary refresh and evaluation: the download task names the stored target
// set instead of carrying it, every target names the frozen vars evaluation by
// digest, and every frozen document stays within its bound.
func TestSubscriptionAdmitsALargeFirstLevelReleaseWithAReferenceFrozenRecord(t *testing.T) {
	initTestNamingVars(t)
	ctx := context.Background()
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	const root = "[Group] Test Show [001-131][Ma10p_1080p]"
	payload, metadata := largeReleaseTorrent(t, root)
	files := make([]acquisitiondomain.ReleaseFile, 0, len(metadata.Files))
	for _, file := range metadata.Files {
		size := file.SizeBytes
		files = append(files, acquisitiondomain.ReleaseFile{SourcePath: file.Path, Filename: file.Name, SizeBytes: &size, MediaKind: file.MediaKind, SourceHash: metadata.InfoHashV1})
	}
	fetcher := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{
		GUID: "large-release", Title: root, Enclosure: "https://downloads.example.test/large.torrent",
		TorrentFilename: metadata.TorrentName, TorrentInfoHash: metadata.InfoHashV1, TorrentInfoHashV1: metadata.InfoHashV1,
		TorrentVersion: metadata.TorrentVersion, TorrentSizeBytes: metadata.TotalSize, TorrentPayload: payload,
		ReleaseFiles: files,
	}}}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	service.SetDownloaderAdmission(&fixedDownloaderAdmission{result: downloadadmission.Result{PathSnapshot: downloaddomain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}}})

	refresh := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	refreshOutcome, err := service.Handle(ctx, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, refresh, workitems.StateSucceeded, refreshOutcome.OwnerResult, refreshOutcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "large-release-worker", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("release evaluation outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(ctx, claim, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	assertReleaseState(t, db, claim.Owner.ID, "accepted_pending_download", "accepted", true)

	var targetCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM acquisition_targets WHERE release_id = $1`, claim.Owner.ID).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if targetCount < 131 {
		t.Fatalf("stored target set has %d targets, want every episode", targetCount)
	}
	var factsRaw []byte
	if err := db.QueryRowContext(ctx, `SELECT frozen_input->'target_facts' FROM download_tasks WHERE release_id = $1`, claim.Owner.ID).Scan(&factsRaw); err != nil {
		t.Fatal(err)
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal(factsRaw, &facts); err != nil {
		t.Fatal(err)
	}
	var reference target.FrozenFacts
	if err := json.Unmarshal(factsRaw, &reference); err != nil {
		t.Fatal(err)
	}
	if _, embedded := facts["targets"]; embedded || reference.Admission != string(target.AdmissionDownload) || reference.TargetCount != targetCount || !strings.HasPrefix(reference.TargetSetDigest, "sha256:") {
		t.Fatalf("frozen target facts = %+v (targets embedded: %t), want the reference to %d stored targets", reference, embedded, targetCount)
	}
	var withVars, withoutDigest int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE render_snapshot ? 'vars'),
       COUNT(*) FILTER (WHERE COALESCE(render_snapshot->>'vars_digest', '') NOT LIKE 'sha256:%')
FROM acquisition_targets WHERE release_id = $1`, claim.Owner.ID).Scan(&withVars, &withoutDigest); err != nil {
		t.Fatal(err)
	}
	if withVars != 0 || withoutDigest != 0 {
		t.Fatalf("render snapshots: %d embed vars, %d do not name a vars digest", withVars, withoutDigest)
	}

	// Every capped document, measured as its column CHECK measures it (the jsonb
	// datum before compression, or the canonical text's octets).
	for _, probe := range []struct {
		name, query string
		limit       int
	}{
		{"releases.internal_facts", `SELECT pg_column_size(internal_facts::text::jsonb) FROM releases WHERE id = $1`, 2097152},
		{"releases.raw_facts", `SELECT pg_column_size(raw_facts::text::jsonb) FROM releases WHERE id = $1`, 2097152},
		{"media_fact_snapshots.release_facts", `SELECT MAX(pg_column_size(release_facts::text::jsonb)) FROM media_fact_snapshots WHERE release_id = $1`, 2097152},
		{"download_tasks.frozen_input.selected_release", `SELECT octet_length((frozen_input->'selected_release')::text) FROM download_tasks WHERE release_id = $1`, 65536},
		{"download_tasks.frozen_input", `SELECT pg_column_size(frozen_input::text::jsonb) FROM download_tasks WHERE release_id = $1`, 1048576},
		{"download_tasks.frozen_policy", `SELECT pg_column_size(frozen_policy::text::jsonb) FROM download_tasks WHERE release_id = $1`, 1048576},
		{"download_tasks.frozen_policy.vars.input", `SELECT octet_length((frozen_policy->'vars'->'input')::text) FROM download_tasks WHERE release_id = $1`, 262144},
		{"work_items.input_snapshot", `SELECT COALESCE(MAX(pg_column_size(w.input_snapshot::text::jsonb)), 0) FROM work_items w JOIN download_tasks d ON d.id = w.owner_id WHERE d.release_id = $1`, 1048576},
		{"acquisition_targets.render_snapshot", `SELECT MAX(pg_column_size(render_snapshot::text::jsonb)) FROM acquisition_targets WHERE release_id = $1`, 1048576},
		{"acquisition_targets.config_snapshot", `SELECT MAX(pg_column_size(config_snapshot::text::jsonb)) FROM acquisition_targets WHERE release_id = $1`, 1048576},
		{"acquisitions.canonical_request", `SELECT octet_length(a.canonical_request::text) FROM acquisitions a JOIN download_tasks d ON d.acquisition_id = a.id WHERE d.release_id = $1`, 65536},
		{"acquisitions.input_canonical_utf8", `SELECT octet_length(a.input_canonical_utf8) FROM acquisitions a JOIN download_tasks d ON d.acquisition_id = a.id WHERE d.release_id = $1`, 1048576},
	} {
		var size int
		if err := db.QueryRowContext(ctx, probe.query, claim.Owner.ID).Scan(&size); err != nil {
			t.Fatalf("measure %s: %v", probe.name, err)
		}
		t.Logf("%-44s size=%7d limit=%7d", probe.name, size, probe.limit)
		if size > probe.limit {
			t.Fatalf("%s measures %d, over its bound %d", probe.name, size, probe.limit)
		}
	}
}
