package application

import (
	"context"
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/testutil/animetest"
)

// PostgreSQL proof that a snapshot reference (SelectedSeedInput without
// locator) is resolved at the admission entry point into exactly the
// locator-form seed: the frozen input snapshot, the DownloadTask frozen seed
// and the replay identity are those of the locator form, a locator-form replay
// of a reference admission (and the reverse) is an exact replay, and a replayed
// reference keeps working after its snapshot was superseded.

type frozenOneShotEvidence struct {
	inputSnapshot string
	requestHash   string
	taskSeed      string
}

func readFrozenOneShotEvidence(t *testing.T, service *oneShotTestServices, oneShotID string) frozenOneShotEvidence {
	t.Helper()
	var evidence frozenOneShotEvidence
	if err := service.db.QueryRowContext(context.Background(), `
SELECT o.input_snapshot::text, o.request_hash, (d.frozen_input->'selected_seed')::text
FROM one_shot_acquisitions o
JOIN download_tasks d ON d.id = o.download_task_id
WHERE o.id = $1`, oneShotID).Scan(&evidence.inputSnapshot, &evidence.requestHash, &evidence.taskSeed); err != nil {
		t.Fatalf("read frozen one-shot evidence: %v", err)
	}
	return evidence
}

// comparableInputSnapshot is the frozen input snapshot with its target facts
// reduced to what does not name the admission's own release: every admission
// mints a new release, and the planned target specs, their observations and
// the set digest carry that release id. The admission outcome and the planned
// target paths must still be identical.
func comparableInputSnapshot(t *testing.T, raw string) string {
	t.Helper()
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatalf("decode input snapshot: %v", err)
	}
	var facts struct {
		Admission  string `json:"admission"`
		ReasonCode string `json:"reason_code"`
		Targets    []struct {
			TargetRef  string `json:"target_ref"`
			SourcePath string `json:"source_path"`
			TargetPath string `json:"target_path"`
			Intent     string `json:"intent"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(snapshot["target_facts"], &facts); err != nil {
		t.Fatalf("decode target facts: %v", err)
	}
	reduced, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	snapshot["target_facts"] = reduced
	out, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestAcquireSeedOnceReferenceFreezesExactlyTheLocatorForm(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	input := humanCandidateForSeed(t, service, seed)
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})

	// The locator form is the verbatim preview row a frontend submits.
	locatorInput := input
	locatorInput.Seed = canonicalSelectedSeed(bindSeedIdentity(seed, snapshot))
	referenceInput := input
	referenceInput.Seed = seedReferenceOf(locatorInput.Seed)

	const locatorKey = "one-shot-reference-locator-0001"
	const referenceKey = "one-shot-reference-reference-0001"
	byLocator, err := service.AcquireSeedOnce(ctx, locatorInput, locatorKey)
	if err != nil {
		t.Fatalf("locator-form admission: %v", err)
	}
	byReference, err := service.AcquireSeedOnce(ctx, referenceInput, referenceKey)
	if err != nil {
		t.Fatalf("reference admission: %v", err)
	}
	if byReference.OneShotID == byLocator.OneShotID {
		t.Fatal("two keys must admit two one-shots")
	}

	locatorEvidence := readFrozenOneShotEvidence(t, service, byLocator.OneShotID)
	referenceEvidence := readFrozenOneShotEvidence(t, service, byReference.OneShotID)
	if comparableInputSnapshot(t, referenceEvidence.inputSnapshot) != comparableInputSnapshot(t, locatorEvidence.inputSnapshot) {
		t.Fatalf("frozen input snapshots differ:\nlocator   %s\nreference %s", locatorEvidence.inputSnapshot, referenceEvidence.inputSnapshot)
	}
	if referenceEvidence.taskSeed != locatorEvidence.taskSeed {
		t.Fatalf("DownloadTask frozen seeds differ:\nlocator   %s\nreference %s", locatorEvidence.taskSeed, referenceEvidence.taskSeed)
	}

	// The stored replay identity of the reference admission is the canonical
	// hash of the locator-form request under the same key.
	snapshotValue := decodeOneShotInputSnapshot([]byte(referenceEvidence.inputSnapshot))
	if snapshotValue == nil || snapshotValue.EffectiveDigest == "" {
		t.Fatalf("reference admission froze no effective digest: %s", referenceEvidence.inputSnapshot)
	}
	expected := locatorInput
	expected.EffectiveDigest = snapshotValue.EffectiveDigest
	wantHash, err := canonicalOneShotRequestHash(expected, referenceKey)
	if err != nil {
		t.Fatal(err)
	}
	if referenceEvidence.requestHash != wantHash {
		t.Fatalf("reference request hash = %s, want the locator-form hash %s", referenceEvidence.requestHash, wantHash)
	}

	// Cross-form replays are exact replays.
	if replayed, err := service.AcquireSeedOnce(ctx, locatorInput, referenceKey); err != nil || replayed.OneShotID != byReference.OneShotID {
		t.Fatalf("locator-form replay of the reference admission = %+v, %v; want %s", replayed, err, byReference.OneShotID)
	}
	if replayed, err := service.AcquireSeedOnce(ctx, referenceInput, locatorKey); err != nil || replayed.OneShotID != byLocator.OneShotID {
		t.Fatalf("reference replay of the locator admission = %+v, %v; want %s", replayed, err, byLocator.OneShotID)
	}

	// A newer snapshot replaces the referenced one and the referenced one
	// expires. The exact replay still returns the original handle (it resolves
	// against the frozen seed, not the feed snapshot), while a new admission of
	// the old reference fails closed with no writes.
	other := seedFeedItem(seed)
	other.GUID = "seed-one-shot-02"
	other.Title = "[Group] Example Anime - 02 [1080p]"
	other.Enclosure = "https://torrent.example.test/one-shot-02.torrent"
	newer := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed), other})
	if newer.SnapshotVersion <= snapshot.SnapshotVersion {
		t.Fatalf("refresh did not supersede the snapshot: v%d after v%d", newer.SnapshotVersion, snapshot.SnapshotVersion)
	}
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshots SET fetched_at = CURRENT_TIMESTAMP - INTERVAL '30 minutes', fresh_until = CURRENT_TIMESTAMP - INTERVAL '15 minutes' WHERE id = $1`, snapshot.SnapshotID); err != nil {
		t.Fatalf("expire the referenced snapshot: %v", err)
	}
	if replayed, err := service.AcquireSeedOnce(ctx, referenceInput, referenceKey); err != nil || replayed.OneShotID != byReference.OneShotID {
		t.Fatalf("replay after supersede = %+v, %v; want the original handle", replayed, err)
	}
	markers := animetest.CountRows(t, db, "one_shot_acquisitions")
	tasks := animetest.CountRows(t, db, "download_tasks")
	if _, err := service.AcquireSeedOnce(ctx, referenceInput, "one-shot-reference-stale-0001"); !isBusinessCode(err, 422, "SEED_PREVIEW_STALE") {
		t.Fatalf("expired reference err = %v, want 422 SEED_PREVIEW_STALE", err)
	}
	unknown := referenceInput
	unknown.Seed = seedReferenceOf(bindSeedIdentity(seed, newer))
	unknown.Seed.StableSeedKey = "seed-not-in-snapshot"
	if _, err := service.AcquireSeedOnce(ctx, unknown, "one-shot-reference-unknown-0001"); !isBusinessCode(err, 422, "SEED_PREVIEW_STALE") {
		t.Fatalf("unknown stable_seed_key err = %v, want 422 SEED_PREVIEW_STALE", err)
	}
	incomplete := referenceInput
	incomplete.Seed.SnapshotContentHash = ""
	if _, err := service.AcquireSeedOnce(ctx, incomplete, "one-shot-reference-incomplete-0001"); !isBusinessCode(err, 422, "SEED_INVALID") {
		t.Fatalf("incomplete reference err = %v, want 422 SEED_INVALID", err)
	}
	// A reference whose content hash does not match its snapshot is stale too.
	mismatched := referenceInput
	mismatched.Seed = seedReferenceOf(bindSeedIdentity(seed, newer))
	mismatched.Seed.SnapshotContentHash = snapshot.ContentHash
	if _, err := service.AcquireSeedOnce(ctx, mismatched, "one-shot-reference-mismatch-0001"); !isBusinessCode(err, 422, "SEED_PREVIEW_STALE") {
		t.Fatalf("mismatched snapshot identity err = %v, want 422 SEED_PREVIEW_STALE", err)
	}
	// A different reference under a used key is a reused key, not a replay.
	moved := referenceInput
	moved.Seed = seedReferenceOf(bindSeedIdentity(seed, newer))
	if _, err := service.AcquireSeedOnce(ctx, moved, referenceKey); !isBusinessCode(err, 409, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("different reference under a used key err = %v, want 409 IDEMPOTENCY_KEY_REUSED", err)
	}
	if got := animetest.CountRows(t, db, "one_shot_acquisitions"); got != markers {
		t.Fatalf("refused references wrote markers: %d -> %d", markers, got)
	}
	if got := animetest.CountRows(t, db, "download_tasks"); got != tasks {
		t.Fatalf("refused references wrote download tasks: %d -> %d", tasks, got)
	}

	// The new snapshot's reference admits the same seed again.
	current := referenceInput
	current.Seed = seedReferenceOf(bindSeedIdentity(seed, newer))
	if _, err := service.AcquireSeedOnce(ctx, current, "one-shot-reference-current-0001"); err != nil {
		t.Fatalf("reference into the current snapshot: %v", err)
	}
}

// The captured current_input variant freezes the same way: the reference and
// locator forms of one seed produce byte-identical input snapshots.
func TestAcquireSeedOnceCurrentInputReferenceFreezesExactlyTheLocatorForm(t *testing.T) {
	service, _, _, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, locatorInput := currentInputForSeed(t, service, seed, nil)
	locatorInput.Seed = canonicalSelectedSeed(bindSeedIdentity(seed, snapshot))
	referenceInput := locatorInput
	referenceInput.Seed = seedReferenceOf(locatorInput.Seed)

	byLocator, err := service.AcquireSeedOnce(ctx, locatorInput, "one-shot-ci-reference-locator-1")
	if err != nil {
		t.Fatalf("locator-form admission: %v", err)
	}
	byReference, err := service.AcquireSeedOnce(ctx, referenceInput, "one-shot-ci-reference-ref-1")
	if err != nil {
		t.Fatalf("reference admission: %v", err)
	}
	locatorEvidence := readFrozenOneShotEvidence(t, service, byLocator.OneShotID)
	referenceEvidence := readFrozenOneShotEvidence(t, service, byReference.OneShotID)
	if comparableInputSnapshot(t, referenceEvidence.inputSnapshot) != comparableInputSnapshot(t, locatorEvidence.inputSnapshot) || referenceEvidence.taskSeed != locatorEvidence.taskSeed {
		t.Fatalf("current_input frozen evidence differs:\nlocator   %s\nreference %s", locatorEvidence.inputSnapshot, referenceEvidence.inputSnapshot)
	}
	if replayed, err := service.AcquireSeedOnce(ctx, locatorInput, "one-shot-ci-reference-ref-1"); err != nil || replayed.OneShotID != byReference.OneShotID {
		t.Fatalf("locator-form replay of the current_input reference = %+v, %v", replayed, err)
	}
}
