package filemaintenance

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
)

type recordingOffsetCandidateScheduler struct {
	batches   [][]string
	recovered []string
	err       error
}

func (s *recordingOffsetCandidateScheduler) ScheduleOffsetReconciliationTx(_ context.Context, _ txscope.Tx, _, _ string, changeIDs []string) error {
	if s.err != nil {
		return s.err
	}
	s.batches = append(s.batches, append([]string(nil), changeIDs...))
	return nil
}

func (s *recordingOffsetCandidateScheduler) ScheduleOffsetRetryRecoveryTx(_ context.Context, _ txscope.Tx, subscriptionID string) error {
	s.recovered = append(s.recovered, subscriptionID)
	return nil
}

// Every change of a subscription crosses one barrier, including a zero-rekey
// change. A change committed after that barrier belongs to the next batch.
func TestPGOffsetCandidateDispatchWaitsForTheWholeSubscriptionBatch(t *testing.T) {
	f := newOffsetFixture(t, ModeAll, domain.CollisionBlock)
	f.seedFile(t, "anime-1", "sub-1", "rel-batch-a", "Show/a.mkv", "tv:e1", "1", Facts{}, "2026-07-01T00:00:00Z")
	f.seedFile(t, "anime-1", "sub-1", "rel-batch-b", "Show/b.mkv", "tv:e1", "", Facts{}, "2026-07-01T00:00:00Z")
	candidates := &recordingOffsetCandidateScheduler{}
	f.scheduler.SetOffsetCandidateScheduler(candidates)

	first, _ := f.changeOffset(t, "src-rel-batch-a", 0, 1)
	second, _ := f.changeOffset(t, "src-rel-batch-b", 0, 1)
	if _, rekeyed, _ := f.changeState(t, first); rekeyed != 1 {
		t.Fatalf("first change rekeyed %d placements, want 1", rekeyed)
	}
	if _, rekeyed, _ := f.changeState(t, second); rekeyed != 0 {
		t.Fatalf("zero-rekey change rekeyed %d placements, want 0", rekeyed)
	}
	if _, err := f.db.Exec(`UPDATE offset_alignment_changes SET state = 'recorded' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE offset_alignment_changes SET state = 'comparing' WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if err := f.scheduler.scheduleOffsetBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(candidates.batches) != 0 {
		t.Fatalf("an unfinished change dispatched %v", candidates.batches)
	}

	if _, err := f.db.Exec(`UPDATE offset_alignment_changes SET state = 'recorded' WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if err := f.scheduler.scheduleOffsetBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(candidates.batches) != 1 || len(candidates.batches[0]) != 2 {
		t.Fatalf("dispatched batches = %v, want one two-change batch", candidates.batches)
	}
	if candidates.batches[0][0] != first || candidates.batches[0][1] != second {
		t.Fatalf("batch ids = %v, want [%s %s]", candidates.batches[0], first, second)
	}
	if got := f.count(t, `SELECT count(*) FROM offset_alignment_changes WHERE candidates_scheduled_at IS NULL`); got != 0 {
		t.Fatalf("unscheduled changes = %d, want 0", got)
	}

	third, _ := f.changeOffset(t, "src-rel-batch-a", 1, 2)
	if _, err := f.db.Exec(`UPDATE offset_alignment_changes SET state = 'recorded' WHERE id = $1`, third); err != nil {
		t.Fatal(err)
	}
	if err := f.scheduler.scheduleOffsetBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(candidates.batches) != 2 || len(candidates.batches[1]) != 1 || candidates.batches[1][0] != third {
		t.Fatalf("next batch = %v, want only %s", candidates.batches, third)
	}
}

// Consecutive edits re-key from the current identity and retain both sides of
// every step; the unresolved alignment request belongs to the newest change.
func TestPGConsecutiveOffsetsKeepEveryAffectedKey(t *testing.T) {
	f := newOffsetFixture(t, ModeAll, domain.CollisionBlock)
	file := f.seedFile(t, "anime-1", "sub-1", "rel-consecutive", "Show/consecutive.mkv", "tv:e2", "2", Facts{}, "2026-07-01T00:00:00Z")
	first, _ := f.changeOffset(t, "src-rel-consecutive", 0, 1)
	second, _ := f.changeOffset(t, "src-rel-consecutive", 1, 4)
	f.expectPlacement(t, file.placementID, "tv:e6", 3, file.path)
	if got := f.count(t, `SELECT count(*) FROM offset_alignment_change_keys WHERE (change_id = $1 AND old_observation_key = 'tv:e2' AND new_observation_key = 'tv:e3') OR (change_id = $2 AND old_observation_key = 'tv:e3' AND new_observation_key = 'tv:e6')`, first, second); got != 2 {
		t.Fatalf("recorded consecutive keys = %d, want 2", got)
	}
	var requestChange string
	if err := f.db.QueryRow(`SELECT offset_change_id FROM alignment_requests WHERE placement_id = $1`, file.placementID).Scan(&requestChange); err != nil {
		t.Fatal(err)
	}
	if requestChange != second {
		t.Fatalf("alignment request change = %s, want latest %s", requestChange, second)
	}
}

// Affected keys are historical batch evidence, not placement-owned rows.
// Retention may clean up the moved placement before candidate dispatch.
func TestPGOffsetChangeKeysSurvivePlacementCleanup(t *testing.T) {
	f := newOffsetFixture(t, ModeAll, domain.CollisionBlock)
	file := f.seedFile(t, "anime-1", "sub-1", "rel-key-cleanup", "Show/cleanup.mkv", "tv:e1", "1", Facts{}, "2026-07-01T00:00:00Z")
	changeID, _ := f.changeOffset(t, "src-rel-key-cleanup", 0, 1)
	if _, err := f.db.Exec(`DELETE FROM placements WHERE id = $1`, file.placementID); err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, `SELECT count(*) FROM offset_alignment_change_keys WHERE change_id = $1 AND old_observation_key = 'tv:e1' AND new_observation_key = 'tv:e2'`, changeID); got != 1 {
		t.Fatalf("surviving change keys = %d, want 1", got)
	}
}

// Stopping freezes a complete batch; resume rediscovers it without another
// offset edit. A failed dispatch rolls its scheduled markers back.
func TestPGOffsetCandidateDispatchRecoversAfterResumeAndRollsBackOnFailure(t *testing.T) {
	f := newOffsetFixture(t, ModeAll, domain.CollisionBlock)
	f.seedFile(t, "anime-1", "sub-1", "rel-batch-resume", "Show/resume.mkv", "tv:e1", "", Facts{}, "2026-07-01T00:00:00Z")
	candidates := &recordingOffsetCandidateScheduler{err: errors.New("dispatch failed")}
	f.scheduler.SetOffsetCandidateScheduler(candidates)
	changeID, _ := f.changeOffset(t, "src-rel-batch-resume", 0, 1)
	if err := f.scheduler.scheduleOffsetBatches(context.Background()); err == nil {
		t.Fatal("a candidate dispatch failure must reach the scheduler")
	}
	if got := f.count(t, `SELECT count(*) FROM offset_alignment_changes WHERE id = $1 AND candidates_scheduled_at IS NULL`, changeID); got != 1 {
		t.Fatal("a failed dispatch must leave the batch discoverable")
	}

	if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:persisted' WHERE id = 'rel-batch-resume'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped' WHERE id = 'sub-1'`); err != nil {
		t.Fatal(err)
	}
	candidates.err = nil
	if err := f.scheduler.scheduleOffsetBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(candidates.batches) != 0 {
		t.Fatal("a stopped subscription must not dispatch candidates")
	}
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active' WHERE id = 'sub-1'`); err != nil {
		t.Fatal(err)
	}
	if err := f.scheduler.scheduleOffsetBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(candidates.batches) != 1 || len(candidates.batches[0]) != 1 || candidates.batches[0][0] != changeID {
		t.Fatalf("resume batches = %v, want %s", candidates.batches, changeID)
	}
	if len(candidates.recovered) != 1 || candidates.recovered[0] != "sub-1" {
		t.Fatalf("recovered subscriptions = %v, want [sub-1]", candidates.recovered)
	}
}
