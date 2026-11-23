package filemaintenance

import (
	"context"
	"testing"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
)

func TestPGSpanChangeRecordsWithoutRekeyingPlacements(t *testing.T) {
	f := newOffsetFixture(t, ModePerGroup, domain.CollisionBlock)
	file := f.seedFile(t, "anime-1", "sub-1", "rel-span", "Show/ep1-2.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "A"}, "2026-07-01T00:00:00Z")
	if _, err := f.db.Exec(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref) VALUES ($1, 'tv:e2', 1, 'candidate-facts-v1', $1 || '-span')`, file.placementID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRowContext(ctx, `UPDATE rss_sources SET episode_span = 2, version = version + 1 WHERE id = 'src-rel-span' RETURNING version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	changeID, created, err := NewOffsetAlignmentService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-1", "src-rel-span", version, 0, 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the span change must create an alignment change record")
	}

	f.expectPlacement(t, file.placementID, "tv:e1,tv:e2", 1, file.path)
	assertFileContent(t, file.path, file.content)
	if state, rekeyed, deleted := f.changeState(t, changeID); state != offsetChangeRecorded || rekeyed != 0 || deleted != 0 {
		t.Fatalf("span change state/rekeyed/deleted = %s/%d/%d, want recorded/0/0", state, rekeyed, deleted)
	}
	var oldSpan, newSpan int
	if err := f.db.QueryRow(`SELECT old_span, new_span FROM offset_alignment_changes WHERE id = $1`, changeID).Scan(&oldSpan, &newSpan); err != nil {
		t.Fatal(err)
	}
	if oldSpan != 1 || newSpan != 2 {
		t.Fatalf("recorded spans = %d/%d, want 1/2", oldSpan, newSpan)
	}
	if n := f.count(t, `SELECT count(*) FROM alignment_requests WHERE offset_change_id = $1`, changeID); n != 0 {
		t.Fatalf("span change created %d placement alignment requests, want 0", n)
	}

	candidates := &recordingOffsetCandidateScheduler{}
	f.scheduler.SetOffsetCandidateScheduler(candidates)
	if err := f.scheduler.scheduleOffsetBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if len(candidates.batches) != 1 || len(candidates.batches[0]) != 1 || candidates.batches[0][0] != changeID {
		t.Fatalf("span reconciliation batches = %v, want only %s", candidates.batches, changeID)
	}
}
