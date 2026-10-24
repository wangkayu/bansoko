package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Contract coverage for workitems.StartStageAttempt (the production gate
// of the inspect and plan handlers) against the migration's fixed-stage primitive
// artifact_start_attempt. The handler wiring itself is covered through the
// real worker entry in artifact/application and stage_attempt_plan_test.go.
//
// Contract pinned here (the artifact_start_attempt primitive):
//   - a grant requires a leased artifact_inspect claim on the same token and
//     expected_owner_version, over an artifact still pending/waiting_retry;
//   - the ticket is the index's last entry plus executor_session/started_at/
//     attempt_until, and carries the session digest of the caller's session;
//   - the same (work, token) is never re-issued (ATTEMPT_ALREADY_ISSUED);
//   - wall and max attempts are strict; a superseded ticket grants no budget
//     and no new work or reset extends the wall.
func TestStartStageAttemptInspectBudgetMinimal(t *testing.T) {
	ctx := context.Background()
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	artifactID := fixture.artifactID

	// The fixture INSERT passes no budget, so the migration must have
	// frozen the DDL DEFAULT/guard values: the canonical artifact_inspect
	// KindPolicy numbers, a wall exactly 86400s after ready, and the initial
	// shape (no attempt, empty index, no ticket). These frozen columns are the
	// budget authority for the rest of the test; they are immutable after
	// INSERT (INSPECT_ATTEMPT_BUDGET_IMMUTABLE), so nothing re-seeds them.
	var policy worker.KindPolicy
	for _, p := range worker.DefaultPolicies() {
		if p.Kind == workitems.KindArtifactInspect {
			policy = p
		}
	}
	if policy.Kind == "" {
		t.Fatal("no artifact_inspect KindPolicy")
	}
	var readyAt, wallUntil time.Time
	var maxAttempts, timeoutSeconds, startedAtSeed int
	var seedIndex string
	var seedTicket sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT inspect_ready_at, inspect_wall_until, inspect_max_attempts, inspect_attempt_timeout_seconds,
       inspect_attempts_started, inspect_attempt_index::text, inspect_attempt_ticket::text
  FROM artifacts WHERE id=$1`, artifactID).Scan(&readyAt, &wallUntil, &maxAttempts, &timeoutSeconds, &startedAtSeed, &seedIndex, &seedTicket); err != nil {
		t.Fatalf("read frozen 183 budget: %v", err)
	}
	if maxAttempts != policy.MaxAttempts || timeoutSeconds != int(policy.BusinessAttemptTimeout().Seconds()) {
		t.Fatalf("frozen budget max=%d timeout=%d want KindPolicy %d/%d", maxAttempts, timeoutSeconds, policy.MaxAttempts, int(policy.BusinessAttemptTimeout().Seconds()))
	}
	if wallUntil.Sub(readyAt) != 86400*time.Second {
		t.Fatalf("frozen wall-ready=%v want 86400s", wallUntil.Sub(readyAt))
	}
	if startedAtSeed != 0 || seedIndex != "[]" || seedTicket.Valid {
		t.Fatalf("frozen initial shape started=%d index=%s ticket=%v", startedAtSeed, seedIndex, seedTicket)
	}

	// Only the owner state is arranged (pending, unverified) so the inspect
	// primitive accepts the claim; the budget columns are left untouched.
	if _, err := db.ExecContext(ctx, `UPDATE artifacts SET state='pending', verified_at=NULL WHERE id=$1`, artifactID); err != nil {
		t.Fatalf("arrange pending artifact: %v", err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	newClaim := func(t *testing.T, key string) workitems.Claim {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerArtifact, ID: artifactID},
			Kind:                 workitems.KindArtifactInspect,
			IdempotencyKey:       key,
			InputSnapshot:        json.RawMessage(`{"schema_version":1}`),
			MaxAttempts:          3,
			ExpectedOwnerVersion: 1,
		})
		if err != nil {
			tx.Rollback()
			t.Fatalf("create artifact_inspect work item: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerTestDB(t).ExecContext(ctx,
			`UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		claim, err := store.ClaimDue(ctx, "stage-attempt-test-worker", []string{workitems.KindArtifactInspect}, time.Minute)
		if err != nil {
			t.Fatalf("claim artifact_inspect work item: %v", err)
		}
		return claim
	}

	// budgetSnapshot reads the persisted budget state so a refused call can be
	// proven to have written nothing.
	type budget struct {
		started  int
		indexLen int
		ticket   string
		wall     time.Time
	}
	readBudget := func() budget {
		var b budget
		if err := db.QueryRowContext(ctx, `
SELECT inspect_attempts_started, jsonb_array_length(inspect_attempt_index),
       COALESCE(inspect_attempt_ticket::text,''), inspect_wall_until
  FROM artifacts WHERE id=$1`, artifactID).Scan(&b.started, &b.indexLen, &b.ticket, &b.wall); err != nil {
			t.Fatal(err)
		}
		return b
	}

	// --- positive ---------------------------------------------------------
	claim := newClaim(t, "artifact_inspect:"+artifactID+":1")
	before := readBudget()
	if before.started != 0 || before.indexLen != 0 || before.ticket != "" {
		t.Fatalf("seed budget must be empty, got %+v", before)
	}
	const session = "stage-attempt-session-1"
	tk, err := store.StartStageAttempt(ctx, claim, session)
	if err != nil {
		t.Fatalf("positive StartStageAttempt: %v", err)
	}
	// The five contract assertions, one by one.
	if tk.WorkItemID != claim.ID {
		t.Fatalf("ticket work_item_id=%q want %q", tk.WorkItemID, claim.ID)
	}
	if tk.Token != claim.FencingToken {
		t.Fatalf("ticket token=%d want %d", tk.Token, claim.FencingToken)
	}
	if tk.Ordinal != 1 {
		t.Fatalf("ticket ordinal=%d want 1", tk.Ordinal)
	}
	if tk.ExecutorSession != session {
		t.Fatalf("ticket executor_session=%q want %q", tk.ExecutorSession, session)
	}
	if tk.State != "issued" || !tk.AttemptUntil.After(tk.StartedAt) {
		t.Fatalf("ticket state=%q until=%v not after started=%v", tk.State, tk.AttemptUntil, tk.StartedAt)
	}
	// ticket ≡ index last entry: the index entry is the ticket minus
	// executor_session/started_at/attempt_until plus the session digest.
	sum := sha256.Sum256([]byte(session))
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	var lastEntry struct {
		WorkItemID    string `json:"work_item_id"`
		Token         int64  `json:"token"`
		Ordinal       int    `json:"ordinal"`
		SessionDigest string `json:"session_digest"`
		State         string `json:"state"`
	}
	var lastEntryRaw json.RawMessage
	if err := db.QueryRowContext(ctx, `
SELECT jsonb_build_object('work_item_id',e->>'work_item_id','token',(e->>'token')::bigint,
        'ordinal',(e->>'ordinal')::int,'session_digest',e->>'session_digest','state',e->>'state')
  FROM artifacts a, jsonb_array_elements(a.inspect_attempt_index) e
 WHERE a.id=$1 ORDER BY (e->>'ordinal')::int DESC LIMIT 1`, artifactID).Scan(&lastEntryRaw); err != nil {
		t.Fatalf("read index last entry: %v", err)
	}
	if err := json.Unmarshal(lastEntryRaw, &lastEntry); err != nil {
		t.Fatalf("decode index last entry: %v", err)
	}
	if lastEntry.WorkItemID != tk.WorkItemID || lastEntry.Token != tk.Token ||
		lastEntry.Ordinal != tk.Ordinal || lastEntry.State != "issued" {
		t.Fatalf("index last entry %+v does not match ticket %+v", lastEntry, tk)
	}
	if lastEntry.SessionDigest != wantDigest {
		t.Fatalf("session_digest=%q want %q", lastEntry.SessionDigest, wantDigest)
	}
	// wall・max strict: attempt_until is the LEAST of now+timeout and the wall.
	if tk.AttemptUntil.After(wallUntil) {
		t.Fatalf("attempt_until=%v exceeds wall=%v", tk.AttemptUntil, wallUntil)
	}
	after := readBudget()
	if after.started != 1 || after.indexLen != 1 || after.ticket == "" {
		t.Fatalf("positive must persist one started attempt, got %+v", after)
	}
	if !after.wall.Equal(wallUntil) {
		t.Fatalf("wall must be frozen: %v vs %v", after.wall, wallUntil)
	}
	t.Logf("sql_positive inspect attempt granted ordinal=1 session=%q digest=%s until=%v (wall frozen %v)",
		session, wantDigest, tk.AttemptUntil, after.wall)

	// --- negative: session out of shape -----------------------------------
	longSession := strings.Repeat("x", 129)
	if _, err := store.StartStageAttempt(ctx, claim, longSession); err == nil {
		t.Fatal("an over-long session must be refused before spending a transaction")
	}
	if got := readBudget(); got != after {
		t.Fatalf("over-long session must write nothing, before=%+v after=%+v", after, got)
	}

	// --- negative: owner kind mismatch ------------------------------------
	mismatched := claim
	mismatched.Owner.Type = workitems.OwnerFileOperation
	if _, err := store.StartStageAttempt(ctx, mismatched, session); err == nil {
		t.Fatal("an owner-kind mismatch must be refused")
	}
	if got := readBudget(); got != after {
		t.Fatalf("owner-kind mismatch must write nothing, before=%+v after=%+v", after, got)
	}

	// --- negative: the same (work, token) is never re-issued --------------
	if _, err := store.StartStageAttempt(ctx, claim, session); !errors.Is(err, workitems.ErrStageAttemptAlreadyIssued) {
		t.Fatalf("the same (work, token) must never be re-issued: %v", err)
	}
	if got := readBudget(); got != after {
		t.Fatalf("replay must write nothing, before=%+v after=%+v", after, got)
	}
	t.Logf("sql_negative replay of the same (work, token) -> granted=false ATTEMPT_ALREADY_ISSUED, zero writes")

	// --- superseded grants no budget; no new work extends the wall --------
	// A second attempt needs a fresh claim: finish the first, then create and
	// claim a second work item over the same artifact.
	if err := store.FinishWithResult(ctx, claim, "succeeded", workitems.OwnerResult{State: "inspected"}, nil); err != nil {
		t.Fatalf("finish the first claim: %v", err)
	}
	second := newClaim(t, "artifact_inspect:"+artifactID+":2")
	grant2, err := store.StartStageAttempt(ctx, second, "stage-attempt-session-2")
	if err != nil {
		t.Fatalf("second StartStageAttempt: %v", err)
	}
	if grant2.Ordinal != 2 {
		t.Fatalf("second grant must be ordinal 2, got %+v", grant2)
	}
	superseded := readBudget()
	if superseded.started != 2 || superseded.indexLen != 2 {
		t.Fatalf("a superseded ticket must not reset the budget: %+v", superseded)
	}
	var firstState string
	if err := db.QueryRowContext(ctx, `
SELECT e->>'state' FROM artifacts a, jsonb_array_elements(a.inspect_attempt_index) e
 WHERE a.id=$1 AND (e->>'ordinal')::int=1`, artifactID).Scan(&firstState); err != nil {
		t.Fatal(err)
	}
	if firstState != "superseded" {
		t.Fatalf("the earlier ticket must be superseded, got %q", firstState)
	}
	if !superseded.wall.Equal(wallUntil) {
		t.Fatalf("a new attempt must not extend the wall: %v vs %v", superseded.wall, wallUntil)
	}
	t.Logf("sql_negative superseded grants no budget (started 1->2, entry 1 superseded) and the wall stays frozen at %v", superseded.wall)
}
