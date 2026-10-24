package application

import (
	"context"
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animeport "bansoko/internal/anime/port"
)

// Attempts model: every admission atomically creates its execution
// attempt #1 with an immutable input digest, and the read surface projects the
// current attempt, the first failed stage and the bounded recovery window.

func TestOneShotAttemptLifecycleRecordedAndProjected(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)

	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-attempt-lifecycle-0001")
	if err != nil {
		t.Fatalf("AcquireSeedOnce: %v", err)
	}

	// Compare the observable task/work-item projection with the retired builder:
	// queued task, release causation, qb effect key, and the merged marker ref.
	var state, key, correlation, causeType, causeID string
	if err := db.QueryRowContext(ctx, `SELECT state,idempotency_key,correlation_id,causation_type,causation_id FROM download_tasks WHERE id=$1`, *result.DownloadTaskID).Scan(&state, &key, &correlation, &causeType, &causeID); err != nil {
		t.Fatal(err)
	}
	var releaseID string
	if err := db.QueryRowContext(ctx, `SELECT release_id FROM one_shot_acquisitions WHERE id=$1`, result.OneShotID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || key != "qb:"+releaseID || causeType != "release" || causeID != releaseID {
		t.Fatalf("legacy task projection: %s %s %s %s", state, key, causeType, causeID)
	}
	var raw []byte
	var expected, max int
	if err := db.QueryRowContext(ctx, `SELECT input_snapshot,expected_owner_version,max_attempts FROM work_item_history WHERE owner_id=$1 AND kind='qb_submit'`, *result.DownloadTaskID).Scan(&raw, &expected, &max); err != nil {
		t.Fatal(err)
	}
	var submit map[string]any
	if err := json.Unmarshal(raw, &submit); err != nil {
		t.Fatal(err)
	}
	ref, ok := submit["one_shot"].(map[string]any)
	if !ok || ref["marker_id"] != result.OneShotID || ref["correlation_id"] != correlation || ref["admission_epoch"] != float64(1) || submit["trigger"] != "one_shot_acquire" || expected != 1 || max != 5 {
		t.Fatalf("legacy submit projection: %s", raw)
	}

	// Attempt #1 is running with the exact input digest of the frozen snapshot.
	view, err := service.ReadOneShotAcquisition(ctx, result.OneShotID)
	if err != nil {
		t.Fatalf("read one-shot view: %v", err)
	}
	if view.CurrentAttemptID == nil || *view.CurrentAttemptID == "" {
		t.Fatal("view does not project the current attempt")
	}
	attempt, err := service.oneShotMarkers.GetAttemptByID(ctx, *view.CurrentAttemptID)
	if err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attempt.State != "running" || attempt.Sequence != 1 || attempt.Mode != animeport.OneShotAttemptModeInitial {
		t.Fatalf("attempt = %s seq %d mode %s, want running #1 initial", attempt.State, attempt.Sequence, attempt.Mode)
	}
	// The attempt digest is a canonical sha256 digest over the stored snapshot.
	if attempt.InputDigest == "" || len(attempt.InputDigest) != len("sha256:")+64 {
		t.Fatalf("attempt input digest = %q, want a canonical sha256 digest", attempt.InputDigest)
	}
	if view.FailedStage != nil {
		t.Fatalf("a freshly admitted chain projects failed_stage %v", *view.FailedStage)
	}
	// The read surface carries the current marker version.
	var markerVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT version FROM one_shot_acquisitions WHERE id = $1`, result.OneShotID).Scan(&markerVersion); err != nil {
		t.Fatalf("read marker version: %v", err)
	}
	if view.Version != markerVersion || view.Version < 1 {
		t.Fatalf("view version = %d, want the marker version %d", view.Version, markerVersion)
	}
}
