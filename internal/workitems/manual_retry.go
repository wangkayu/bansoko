package workitems

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/persistence/txscope"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// ManualRetry creates a new WorkItem row for the same unresolved external
// effect as a terminal failed item. It preserves the existing behavior for
// owners that do not need a product-state reset.
func (s *Store) ManualRetry(ctx context.Context, owner OwnerRef, failedWorkItemID, reason, requestIdempotencyKey string, readOwner OwnerReader) (string, error) {
	return s.ManualRetryWithOwnerReset(ctx, owner, failedWorkItemID, reason, requestIdempotencyKey, readOwner, nil)
}

// ManualRetryWithOwnerReset atomically resets an owner, creates its replacement
// WorkItem, and records the HTTP action idempotency result. The reset callback
// runs only after an unseen request key is established, so a replay never
// resets the owner a second time. Callers still cannot provide an effect key;
// the replacement derives it from the failed WorkItem.
func (s *Store) ManualRetryWithOwnerReset(ctx context.Context, owner OwnerRef, failedWorkItemID, reason, requestIdempotencyKey string, readOwner OwnerReader, resetOwner OwnerResetter, canonicalizers ...ManualRetryInputCanonicalizer) (string, error) {
	return s.ManualRetryWithOwnerResetPreflight(ctx, owner, failedWorkItemID, reason, requestIdempotencyKey, readOwner, resetOwner, nil, canonicalizers...)
}

// OwnerPreflight runs inside the manual-retry transaction before any owner or
// WorkItem lock. Callers use it to take the lifecycle row lock first so manual
// retry shares the deterministic lock order retirement and every other visible
// ManagedAnime mutation use (no 40P01 deadlock).
type OwnerPreflight func(ctx context.Context, scope txscope.Tx) error

// ManualRetryWithOwnerResetPreflight is ManualRetryWithOwnerReset with a
// lifecycle-first preflight.
// ManualRetryWithOwnerResetPreflight is ManualRetryWithOwnerReset with a
// lifecycle-first preflight.
//
// The whole entry is bounded-retried as ONE transaction. Only a
// 55P03 recognised from the trusted fixed prelock phase (marker? -> lifecycle ->
// advisory -> failed WorkItem -> owner row) classifies as contention and
// triggers the next complete attempt (20/40/80/160ms, context-cancellable,
// never a savepoint partial retry, never sleeping with locks held). Every other
// error — a business rejection, a version conflict, a Commit unknown — returns
// immediately. Exhaustion yields the typed ErrManualRetryContention, which the
// application adapter maps to its own zero-write 409; it never shares the
// retirement reason code.
func (s *Store) ManualRetryWithOwnerResetPreflight(ctx context.Context, owner OwnerRef, failedWorkItemID, reason, requestIdempotencyKey string, readOwner OwnerReader, resetOwner OwnerResetter, preflight OwnerPreflight, canonicalizers ...ManualRetryInputCanonicalizer) (string, error) {
	var lastErr error
	for attempt := 0; attempt < manualRetryRetryBudget; attempt++ {
		newID, err := s.manualRetryAttempt(ctx, owner, failedWorkItemID, reason, requestIdempotencyKey, readOwner, resetOwner, preflight, canonicalizers...)
		if err == nil {
			return newID, nil
		}
		if !errors.Is(err, ErrManualRetryContention) {
			return "", err
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// No sleep after the final attempt: the budget is spent, and waiting
		// 320ms only to give up immediately is pure latency on an already
		// contended entry.
		if attempt+1 < manualRetryRetryBudget {
			if waitErr := waitForManualRetryRetry(ctx, attempt); waitErr != nil {
				return "", waitErr
			}
		}
	}
	_ = lastErr // exhaustion is always a 55P03 (only contention reaches here)
	return "", fmt.Errorf("%w: fixed lock set contended after %d attempts", ErrManualRetryContention, manualRetryRetryBudget)
}

// manualRetryAttempt is ONE complete attempt: every failure path rolls the whole
// transaction back (defer) before the caller may retry.
func (s *Store) manualRetryAttempt(ctx context.Context, owner OwnerRef, failedWorkItemID, reason, requestIdempotencyKey string, readOwner OwnerReader, resetOwner OwnerResetter, preflight OwnerPreflight, canonicalizers ...ManualRetryInputCanonicalizer) (string, error) {
	if err := owner.validate(); err != nil {
		return "", err
	}
	if !manualRetryOwnerTypes[owner.Type] {
		return "", fmt.Errorf("%w: owner type %q does not accept manual retry", ErrInvalidSpec, owner.Type)
	}
	if strings.TrimSpace(failedWorkItemID) == "" {
		return "", fmt.Errorf("%w: failed work item ID is required", ErrInvalidSpec)
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 512 {
		return "", fmt.Errorf("%w: manual retry reason is required and bounded", ErrInvalidSpec)
	}
	if len(strings.TrimSpace(requestIdempotencyKey)) < 8 || len(requestIdempotencyKey) > 128 {
		return "", fmt.Errorf("%w: request idempotency key is required and bounded", ErrInvalidSpec)
	}
	requestHash := hashManualRetryRequest(owner, failedWorkItemID, reason)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin manual retry transaction: %w", err)
	}
	defer tx.Rollback()
	queries := persistence.New(tx)

	// Read-only discovery of the failed item decides the lock order before any
	// lock is taken. A one-shot failed item carries its marker reference in the
	// input snapshot; an ordinary item does not. This read never locks the failed
	// row, so a corrupt reference aborts the retry with zero writes.
	discoveredSnapshot, discoveryErr := queries.GetFailedWorkItemSnapshot(ctx, persistence.GetFailedWorkItemSnapshotParams{
		ID:        failedWorkItemID,
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if discoveryErr != nil && !errors.Is(discoveryErr, sql.ErrNoRows) {
		return "", fmt.Errorf("read failed work item: %w", discoveryErr)
	}
	var oneShotRef *OneShotRef
	if discoveryErr == nil {
		oneShotRef, err = parseOneShotRef(json.RawMessage(discoveredSnapshot))
		if err != nil {
			return "", fmt.Errorf("read failed one-shot reference: %w", err)
		}
	}

	// One-shot branch: the marker is locked first, matching one-shot
	// admission/reclamation. Its running+epoch gate rejects a reclaiming or
	// epoch-advanced marker before anything else is touched.
	var lockedMarker oneShotMarkerRow
	if oneShotRef != nil {
		lockedMarker, err = lockOneShotMarkerTx(ctx, tx, oneShotRef.MarkerID, oneShotRef.AdmissionEpoch)
		if err != nil {
			return "", err
		}
	}

	// Ordinary branch: lifecycle first. One-shot: lifecycle second, right after
	// the marker. Either way the order is marker?-> lifecycle -> idempotency ->
	// WorkItem -> owner, shared with retirement and reclaim so a concurrent
	// transition yields a typed conflict, never a 40P01 deadlock.
	if preflight != nil {
		if err := preflight(ctx, txscope.Wrap(tx)); err != nil {
			return "", err
		}
	}

	if err := queries.LockManualRetryIdempotency(ctx, "bansoko:manual-retry:"+owner.Type+":"+owner.ID+":"+requestIdempotencyKey); err != nil {
		return "", fmt.Errorf("lock manual retry idempotency key: %w", err)
	}

	// Fixed prelock set, NOWAIT, before any business write: the
	// failed WorkItem and the owner row this entry's version reader already
	// locks. Any 55P03 rolls the ENTIRE transaction back (defer) — releasing the
	// marker, the lifecycle lock, the advisory and every row held here — and the
	// caller retries the whole entry bounded.
	if err := lockManualRetrySetTx(ctx, tx, owner, failedWorkItemID); err != nil {
		return "", err
	}

	// A repeated HTTP request with the same key replays the original result; a
	// key reused with a different body is a typed conflict. This check precedes
	// the owner reset so replay is side-effect free.
	existing, err := queries.GetManualRetryIdempotency(ctx, persistence.GetManualRetryIdempotencyParams{
		OwnerType:      sql.NullString{String: owner.Type, Valid: true},
		OwnerID:        sql.NullString{String: owner.ID, Valid: true},
		IdempotencyKey: requestIdempotencyKey,
	})
	if err == nil {
		if existing.RequestHash != requestHash {
			return "", ErrIdempotencyConflict
		}
		if !existing.ResultWorkItemID.Valid {
			return "", fmt.Errorf("read manual retry idempotency: result work item ID is null")
		}
		// Replay only while the recorded result owner still exists. Local cleanup
		// deletes the owner graph and its idempotency evidence together; a
		// lingering key must never return a success for a removed owner.
		var present int
		if err := tx.QueryRowContext(ctx, `
SELECT 1 FROM work_item_history WHERE id = $1 AND owner_type = $2 AND owner_id = $3`,
			existing.ResultWorkItemID.String, owner.Type, owner.ID).Scan(&present); errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		} else if err != nil {
			return "", fmt.Errorf("read manual retry result owner: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("commit manual retry replay: %w", err)
		}
		return existing.ResultWorkItemID.String, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read manual retry idempotency: %w", err)
	}

	// No replay and no failed row: this request names nothing retryable.
	if errors.Is(discoveryErr, sql.ErrNoRows) {
		return "", ErrNotFound
	}

	// The failed item must be a terminal failed row for this owner; its effect
	// key, typed kind, and input snapshot seed the new row. In the one-shot
	// branch this lock is intentionally acquired after the marker lock; ordinary
	// rows retain the lifecycle-first path.
	locked, err := queries.LockFailedWorkItemForManualRetry(ctx, persistence.LockFailedWorkItemForManualRetryParams{
		ID:        failedWorkItemID,
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock failed work item: %w", err)
	}
	kind := locked.Kind
	effectKey := locked.IdempotencyKey
	maxAttempts := int(locked.MaxAttempts)
	state := locked.State
	snapshot := locked.InputSnapshot
	if state != StateFailed {
		return "", fmt.Errorf("%w: work item %s is not in a failed state", ErrInvalidSpec, failedWorkItemID)
	}
	currentRef, err := parseOneShotRef(json.RawMessage(snapshot))
	if err != nil {
		return "", fmt.Errorf("read locked failed one-shot reference: %w", err)
	}
	if oneShotRef == nil && currentRef != nil {
		// This can only be reached if the row changed between the discovery read
		// and the lock. Do not let a newly declared marker bypass marker-first
		// admission.
		return "", fmt.Errorf("%w: one-shot input requires marker-first retry admission", ErrInvalidSpec)
	}
	if oneShotRef != nil {
		if currentRef == nil || *currentRef != *oneShotRef {
			return "", fmt.Errorf("%w: failed one-shot input changed while retry was admitted", ErrInvalidOneShotClaim)
		}
	}

	ownerVersion := int64(1)
	if readOwner != nil {
		ownerVersion, err = readOwner(ctx, txscope.Wrap(tx))
		if err != nil {
			return "", fmt.Errorf("read owner version: %w", err)
		}
	}
	if ownerVersion < 1 {
		return "", fmt.Errorf("%w: owner version must be at least 1", ErrInvalidSpec)
	}
	if resetOwner != nil && readOwner == nil {
		return "", fmt.Errorf("%w: owner reset requires an owner version reader", ErrInvalidSpec)
	}
	newOwnerVersion := ownerVersion
	if resetOwner != nil {
		newOwnerVersion, err = resetOwner(ctx, txscope.Wrap(tx), ownerVersion)
		if err != nil {
			return "", fmt.Errorf("reset owner for manual retry: %w", err)
		}
		if newOwnerVersion <= ownerVersion {
			return "", fmt.Errorf("%w: owner reset must advance the owner version", ErrInvalidSpec)
		}
	}
	if len(canonicalizers) > 0 && canonicalizers[0] != nil {
		snapshot, err = canonicalizers[0](ctx, txscope.Wrap(tx), owner, kind, json.RawMessage(snapshot))
		if err != nil {
			return "", fmt.Errorf("canonicalize manual retry input: %w", err)
		}
	}

	spec := WorkItemSpec{
		Owner:                owner,
		Kind:                 kind,
		IdempotencyKey:       effectKey,
		InputSnapshot:        json.RawMessage(snapshot),
		MaxAttempts:          maxAttempts,
		ExpectedOwnerVersion: newOwnerVersion,
	}
	var newID string
	if oneShotRef != nil {
		// The marker is already locked and validated above. Do not re-lock it:
		// marker -> failed WorkItem -> owner -> insertion is the required order.
		// A distinct live item stays ErrLiveItemConflict — never a silent merge.
		newID, err = s.createOneShotMarkerLocked(ctx, tx, lockedMarker, *oneShotRef, spec)
	} else {
		newID, err = s.Create(ctx, tx, spec)
	}
	if err != nil {
		return "", err
	}
	if oneShotRef != nil {
		// The marker lock is still held from the marker-first preflight. Reassign
		// an outstanding ambiguous admission only after the replacement insert
		// succeeds, so a live-item conflict or any owner/canonicalizer failure
		// rolls back both the owner reset and the ledger continuation. The
		// admission id and all evidence remain unchanged for gate replay.
		if err := reassignOneShotLeaseTx(ctx, tx, oneShotRef.MarkerID, lockedMarker, failedWorkItemID, newID, owner, oneShotRef.AdmissionEpoch); err != nil {
			return "", fmt.Errorf("continue one-shot admission lease: %w", err)
		}
	}
	resultSnapshot, err := json.Marshal(map[string]any{
		"work_item_id":        newID,
		"failed_work_item_id": failedWorkItemID,
		"state":               StateAvailable,
	})
	if err != nil {
		return "", fmt.Errorf("encode manual retry result: %w", err)
	}
	if err := queries.RecordManualRetryIdempotency(ctx, persistence.RecordManualRetryIdempotencyParams{
		IdempotencyKey:   requestIdempotencyKey,
		RequestHash:      requestHash,
		OwnerType:        sql.NullString{String: owner.Type, Valid: true},
		OwnerID:          sql.NullString{String: owner.ID, Valid: true},
		FailedWorkItemID: sql.NullString{String: failedWorkItemID, Valid: true},
		ResultWorkItemID: sql.NullString{String: newID, Valid: true},
		ResultSnapshot:   json.RawMessage(resultSnapshot),
	}); err != nil {
		return "", fmt.Errorf("record manual retry idempotency: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit manual retry: %w", err)
	}
	s.logEvent(owner, newID, "manual_retry", "work_item_manual_retry_created", "reason_code=MANUAL_RETRY idempotency_key_hash="+keyHash(effectKey), 0)
	return newID, nil
}

func hashManualRetryRequest(owner OwnerRef, failedWorkItemID, reason string) string {
	digest := sha256.Sum256([]byte(owner.Type + "\x00" + owner.ID + "\x00" + failedWorkItemID + "\x00" + reason))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// ErrManualRetryContention marks a complete fixed-lock-set conflict for the
// manual-retry entry alone. It never shares the retirement or checkpoint
// contention codes (each stage registers entry-specific reasons, never
// ErrRetirementContention / MANAGED_ANIME_RETIREMENT_BUSY). The HTTP 409 mapping
// lives in the application adapter, never here.
var ErrManualRetryContention = errors.New("manual retry lock set contention")

// manualRetryRetryBudget is the bounded whole-transaction retry: at
// most 5 complete attempts (including the first), 20/40/80/160ms backoff,
// context-cancellable. Only a 55P03 recognised from the trusted fixed prelock
// phase may trigger a full retry; a business error, an unknown effect, or a
// 40P01 never retries.
const manualRetryRetryBudget = 5

func manualRetryRetryDelay(attempt int) time.Duration {
	return 20 * time.Millisecond << attempt // 20/40/80/160ms
}

// waitForManualRetryRetry sleeps the bounded backoff for one retry attempt,
// cancelling with the caller's context (never sleeping with locks held: the
// whole transaction already rolled back before this wait).
func waitForManualRetryRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(manualRetryRetryDelay(attempt))
	defer timer.Stop() // no WaitGroup exists here: this defer stops THIS function's own timer only
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// manualRetryContentionError classifies a prelock-phase failure as retryable
// set contention. Only SQLSTATE 55P03 from the fixed NOWAIT statements
// qualifies; every other error is returned unchanged.
func manualRetryContentionError(err error) error {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "55P03" {
		return fmt.Errorf("%w: %w", ErrManualRetryContention, err)
	}
	return err
}

// manualRetryOwnerLock is the single owner row one manual-retry owner type
// owns. It is derived from the owner allowlist (types.go manualRetryOwnerTypes),
// never from a caller-supplied ID list: the store knows which table and which
// key column each admitted owner type addresses.
type manualRetryOwnerLock struct {
	stmt string
}

// manualRetryOwnerLocks maps every owner type the manual-retry entry admits to
// the single owner row its version reader already locks. The prelock takes the
// same row NOWAIT first, so the reader's own FOR UPDATE becomes a no-op and the
// entry never blocks on an owner row after it has started writing.
var manualRetryOwnerLocks = map[string]manualRetryOwnerLock{
	OwnerRSSSourceRuntime: {stmt: `SELECT rss_source_id FROM rss_source_runtime WHERE rss_source_id=$1 FOR UPDATE NOWAIT`},
	OwnerDownloadTask:     {stmt: `SELECT id FROM download_tasks WHERE id=$1 FOR UPDATE NOWAIT`},
	OwnerArtifact:         {stmt: `SELECT id FROM artifacts WHERE id=$1 FOR UPDATE NOWAIT`},
}

// lockManualRetrySetTx is the fixed prelock: the complete
// contention domain of one manual retry that this entry owns, every row
// addressed by a trusted owner relation, all NOWAIT, before any business write.
//
// The order preserves the entry's established prefix exactly: marker? ->
// lifecycle -> advisory (all taken by the caller before this runs) -> failed
// work item -> owner row. The advisory is deliberately NOT moved ahead of the
// marker/lifecycle prefix here: its key already embeds the owner ID, so two
// contending requests necessarily share the marker and lifecycle rows too, and
// the one that loses those rows can never reach the advisory. (W3's advisory key
// does not embed the release ID, which is why that entry takes it first.)
//
// The idempotency record needs no prelock of its own: the advisory already
// serialises every request carrying this key, and the record is only inserted
// at the end of the transaction.
//
// Any 55P03 aborts the whole transaction (the caller's defer rolls back,
// releasing the marker, the lifecycle lock, the advisory and every row held
// here) and classifies as bounded-retry contention; every other error keeps its
// typed identity and is never retried.
func lockManualRetrySetTx(ctx context.Context, tx *sql.Tx, owner OwnerRef, failedWorkItemID string) error {
	// The work-item row lock goes through the registered SECURITY DEFINER
	// primitive: work_items has UPDATE revoked from the runtime role, so a raw
	// SELECT ... FOR UPDATE NOWAIT from the application is refused with 42501.
	// The prelock calls the NOWAIT twin of the frozen blocking function; the
	// frozen function keeps its own grants and is untouched.
	//
	// A missing row must stay a business NotFound decided by the later typed
	// lock, not by this prelock, so the result rows are discarded rather than
	// scanned: replaying a request whose work item was cleaned up must still
	// replay instead of turning into a NotFound.
	if _, err := tx.ExecContext(ctx, `SELECT * FROM public.work_item_lock_failed_retry_nowait($1,$2,$3)`, failedWorkItemID, owner.Type, owner.ID); err != nil {
		return manualRetryContentionError(fmt.Errorf("prelock failed work item: %w", err))
	}
	lock, ok := manualRetryOwnerLocks[owner.Type]
	if !ok {
		// Not an owner row this entry locks: the reader rejects the type before
		// any write, so there is nothing to prelock.
		return nil
	}
	if _, err := tx.ExecContext(ctx, lock.stmt, owner.ID); err != nil {
		return manualRetryContentionError(fmt.Errorf("prelock %s owner: %w", owner.Type, err))
	}
	return nil
}
