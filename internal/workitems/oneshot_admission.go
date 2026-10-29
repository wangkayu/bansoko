package workitems

// Finite one-shot effect-admission protocol.
//
// Lock order: every one-shot path serializes as marker -> WorkItem -> owner.
// Ordinary Finish/Retry lock the WorkItem first, so one-shot claims use the
// dedicated marker-first transition methods below instead of retrofitting
// settlement onto them.
//
// The marker row (one_shot_acquisitions) owns admission_epoch,
// active_effect_count and the bounded active_effect_leases JSONB ledger.
// active_effect_count must always equal the ledger cardinality (DB CHECK).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/persistence/txscope"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// AdmissionLease is one unconsumed entry of the marker-owned admission ledger.
// Entries carry opaque references only, never raw payloads. The JSON shape
// matches the shared ledger contract (admission_id, work_item_id, owner_type,
// owner_id, claimed_admission_epoch, admitted_at).
type AdmissionLease struct {
	MarkerID     string    `json:"-"`
	AdmissionID  string    `json:"admission_id"`
	WorkItemID   string    `json:"work_item_id"`
	OwnerType    string    `json:"owner_type"`
	OwnerID      string    `json:"owner_id"`
	ClaimedEpoch int64     `json:"claimed_admission_epoch"`
	AdmittedAt   time.Time `json:"admitted_at"`
}

// oneShotLedgerEntry mirrors the stored JSON of one ledger entry.
type oneShotLedgerEntry struct {
	AdmissionID  string `json:"admission_id"`
	WorkItemID   string `json:"work_item_id"`
	OwnerType    string `json:"owner_type"`
	OwnerID      string `json:"owner_id"`
	ClaimedEpoch int64  `json:"claimed_admission_epoch"`
	AdmittedAt   string `json:"admitted_at"`
}

// parseOneShotRef extracts the one_shot marker reference from an input
// snapshot. A snapshot without the key returns (nil, nil); a malformed or
// incomplete object is a typed error so a corrupt snapshot can never bypass
// the admission gate.
func parseOneShotRef(snapshot json.RawMessage) (*OneShotRef, error) {
	trimmed := bytes.TrimSpace(snapshot)
	if len(trimmed) == 0 {
		// Create normalizes an omitted snapshot to {}, and a few callers use the
		// parser before that normalization. Preserve the existing empty-input
		// convention; a non-empty malformed JSON value is still fail-closed
		// below.
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("%w: input snapshot must be a JSON object: %v", ErrInvalidOneShotClaim, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: input snapshot must be a JSON object", ErrInvalidOneShotClaim)
	}
	raw, ok := object["one_shot"]
	if !ok {
		return nil, nil
	}
	trimmedRef := bytes.TrimSpace(raw)
	if len(trimmedRef) == 0 || bytes.Equal(trimmedRef, []byte("null")) || trimmedRef[0] != '{' {
		return nil, fmt.Errorf("%w: one_shot marker reference must be a JSON object", ErrInvalidOneShotClaim)
	}
	var ref OneShotRef
	if err := json.Unmarshal(trimmedRef, &ref); err != nil {
		return nil, fmt.Errorf("%w: one_shot marker reference is malformed: %v", ErrInvalidOneShotClaim, err)
	}
	if err := ref.validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidOneShotClaim, err)
	}
	return &ref, nil
}

// ParseOneShotRef extracts the optional one-shot marker reference from a
// WorkItem input snapshot. A missing one_shot field returns (nil, nil); a
// malformed or incomplete marker is returned as ErrInvalidOneShotClaim. Keep
// this parser at the WorkItem boundary so owner applications do not duplicate
// the snapshot JSON semantics.
func ParseOneShotRef(snapshot json.RawMessage) (*OneShotRef, error) {
	return parseOneShotRef(snapshot)
}

// mergeOneShotSnapshot injects the canonical one_shot object into the input
// snapshot. An existing but different reference is rejected; an identical one
// is preserved unchanged.
func mergeOneShotSnapshot(snapshot json.RawMessage, ref *OneShotRef) (json.RawMessage, error) {
	base := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(snapshot)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(snapshot, &base); err != nil {
			return nil, fmt.Errorf("%w: input snapshot must be a JSON object to carry a one-shot reference", ErrInvalidSpec)
		}
		if base == nil {
			return nil, fmt.Errorf("%w: input snapshot must be a JSON object to carry a one-shot reference", ErrInvalidSpec)
		}
	}
	if raw, ok := base["one_shot"]; ok {
		// The stored value is the reference object itself, not a wrapper.
		var existing OneShotRef
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("%w: existing one_shot object is malformed", ErrInvalidSpec)
		}
		if err := existing.validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSpec, err)
		}
		if existing != *ref {
			return nil, fmt.Errorf("%w: input snapshot already carries a different one_shot reference", ErrInvalidSpec)
		}
		return snapshot, nil
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		return nil, fmt.Errorf("encode one-shot reference: %w", err)
	}
	base["one_shot"] = encoded
	merged, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("merge one-shot reference into input snapshot: %w", err)
	}
	return merged, nil
}

type oneShotMarkerRow struct {
	State             string
	Epoch             int64
	Version           int64
	ActiveEffectCount int32
	Leases            []oneShotLedgerEntry
	LeasesRaw         json.RawMessage
}

func oneShotMarkerRowFromGenerated(generatedRow persistence.LockOneShotMarkerRow) (oneShotMarkerRow, error) {
	row := oneShotMarkerRow{
		State:             generatedRow.State,
		Epoch:             generatedRow.AdmissionEpoch,
		Version:           generatedRow.Version,
		ActiveEffectCount: generatedRow.ActiveEffectCount,
		LeasesRaw:         generatedRow.ActiveEffectLeases,
	}
	if len(generatedRow.ActiveEffectLeases) > 0 && string(generatedRow.ActiveEffectLeases) != "null" {
		if err := json.Unmarshal(generatedRow.ActiveEffectLeases, &row.Leases); err != nil {
			return oneShotMarkerRow{}, fmt.Errorf("decode one-shot admission ledger: %w", err)
		}
	}
	if err := validateOneShotMarkerLedger(row); err != nil {
		return oneShotMarkerRow{}, fmt.Errorf("decode one-shot admission ledger: %w", err)
	}
	return row, nil
}

func lockOneShotMarkerForUpdateTx(ctx context.Context, tx *sql.Tx, markerID string) (oneShotMarkerRow, error) {
	generatedRow, err := persistence.New(tx).LockOneShotMarker(ctx, markerID)
	if errors.Is(err, sql.ErrNoRows) {
		return oneShotMarkerRow{}, ErrOneShotMarkerMissing
	}
	if err != nil {
		return oneShotMarkerRow{}, fmt.Errorf("lock one-shot marker: %w", err)
	}
	return oneShotMarkerRowFromGenerated(generatedRow)
}

func parseCanonicalOneShotTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || value != parsed.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, fmt.Errorf("non-canonical admitted_at")
	}
	return parsed.UTC(), nil
}

func validateOneShotOwnerContext(markerID string, entry oneShotLedgerEntry, owner OwnerRef, ref *OneShotRef, claimedEpoch int64) error {
	if err := owner.validate(); err != nil {
		return fmt.Errorf("%w: expected owner is invalid: %v", ErrInvalidOneShotLedger, err)
	}
	if ref == nil {
		return fmt.Errorf("%w: persisted one-shot reference is missing", ErrInvalidOneShotLedger)
	}
	if err := ref.validate(); err != nil {
		return fmt.Errorf("%w: persisted one-shot reference is invalid: %v", ErrInvalidOneShotLedger, err)
	}
	if entry.OwnerType != owner.Type || entry.OwnerID != owner.ID ||
		ref.MarkerID != markerID || ref.AdmissionEpoch != claimedEpoch || entry.ClaimedEpoch != claimedEpoch {
		return fmt.Errorf("%w: persisted one-shot owner context does not match admission", ErrInvalidOneShotLedger)
	}
	return nil
}

func validateOneShotLedger(count int32, entries []oneShotLedgerEntry) error {
	if count < 0 {
		return fmt.Errorf("%w: active effect count cannot be negative", ErrInvalidOneShotLedger)
	}
	if int64(count) != int64(len(entries)) {
		return fmt.Errorf("%w: active effect count %d does not match ledger cardinality %d", ErrInvalidOneShotLedger, count, len(entries))
	}
	admissionIDs := make(map[string]struct{}, len(entries))
	pairs := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		if strings.TrimSpace(entry.AdmissionID) == "" || strings.TrimSpace(entry.WorkItemID) == "" ||
			strings.TrimSpace(entry.OwnerType) == "" || strings.TrimSpace(entry.OwnerID) == "" {
			return fmt.Errorf("%w: ledger entry %d has blank identity", ErrInvalidOneShotLedger, index)
		}
		if err := (OwnerRef{Type: entry.OwnerType, ID: entry.OwnerID}).validate(); err != nil {
			return fmt.Errorf("%w: ledger entry %d has invalid owner: %v", ErrInvalidOneShotLedger, index, err)
		}
		if entry.ClaimedEpoch < 1 {
			return fmt.Errorf("%w: ledger entry %d has invalid claimed epoch", ErrInvalidOneShotLedger, index)
		}
		_, err := parseCanonicalOneShotTimestamp(entry.AdmittedAt)
		if err != nil {
			return fmt.Errorf("%w: ledger entry %d has non-canonical admitted_at", ErrInvalidOneShotLedger, index)
		}
		if _, exists := admissionIDs[entry.AdmissionID]; exists {
			return fmt.Errorf("%w: duplicate admission id %q", ErrInvalidOneShotLedger, entry.AdmissionID)
		}
		admissionIDs[entry.AdmissionID] = struct{}{}
		pair := entry.WorkItemID + "\x00" + strconv.FormatInt(entry.ClaimedEpoch, 10)
		if _, exists := pairs[pair]; exists {
			return fmt.Errorf("%w: duplicate work item and epoch pair", ErrInvalidOneShotLedger)
		}
		pairs[pair] = struct{}{}
	}
	return nil
}

func validateOneShotMarkerLedger(row oneShotMarkerRow) error {
	return validateOneShotLedger(row.ActiveEffectCount, row.Leases)
}

func validateAdmissionLeaseShape(lease AdmissionLease) error {
	if strings.TrimSpace(lease.MarkerID) == "" || strings.TrimSpace(lease.AdmissionID) == "" ||
		strings.TrimSpace(lease.WorkItemID) == "" || strings.TrimSpace(lease.OwnerType) == "" ||
		strings.TrimSpace(lease.OwnerID) == "" || lease.ClaimedEpoch < 1 || lease.AdmittedAt.IsZero() {
		return fmt.Errorf("%w: admission lease identity is incomplete", ErrInvalidOneShotClaim)
	}
	if err := (OwnerRef{Type: lease.OwnerType, ID: lease.OwnerID}).validate(); err != nil {
		return fmt.Errorf("%w: admission lease owner is invalid: %v", ErrInvalidOneShotClaim, err)
	}
	return nil
}

func leaseEntryToAdmissionLease(markerID string, entry oneShotLedgerEntry) (AdmissionLease, error) {
	admittedAt, err := parseCanonicalOneShotTimestamp(entry.AdmittedAt)
	if err != nil {
		return AdmissionLease{}, fmt.Errorf("%w: persisted admission timestamp is invalid", ErrInvalidOneShotLedger)
	}
	lease := AdmissionLease{
		MarkerID:     markerID,
		AdmissionID:  entry.AdmissionID,
		WorkItemID:   entry.WorkItemID,
		OwnerType:    entry.OwnerType,
		OwnerID:      entry.OwnerID,
		ClaimedEpoch: entry.ClaimedEpoch,
		AdmittedAt:   admittedAt,
	}
	if err := validateAdmissionLeaseShape(lease); err != nil {
		return AdmissionLease{}, fmt.Errorf("%w: persisted admission lease is invalid: %v", ErrInvalidOneShotLedger, err)
	}
	return lease, nil
}

func validateClaimLeaseIdentity(claim Claim, lease AdmissionLease) error {
	if err := validateOneShotClaimContext(claim); err != nil {
		return err
	}
	if err := validateAdmissionLeaseShape(lease); err != nil {
		return err
	}
	if lease.MarkerID != claim.OneShot.MarkerID || lease.WorkItemID != claim.ID ||
		lease.ClaimedEpoch != claim.OneShot.AdmissionEpoch ||
		lease.OwnerType != claim.Owner.Type || lease.OwnerID != claim.Owner.ID {
		return fmt.Errorf("%w: admission lease identity does not match claim", ErrInvalidOneShotClaim)
	}
	return nil
}

func findOneShotLease(row oneShotMarkerRow, admissionID string) (oneShotLedgerEntry, bool, error) {
	var match oneShotLedgerEntry
	found := false
	for _, entry := range row.Leases {
		if entry.AdmissionID != admissionID {
			continue
		}
		if found {
			return oneShotLedgerEntry{}, false, fmt.Errorf("%w: duplicate admission id %q", ErrInvalidOneShotLedger, admissionID)
		}
		match = entry
		found = true
	}
	return match, found, nil
}

func validateOneShotClaimContext(claim Claim) error {
	if claim.OneShot == nil {
		return fmt.Errorf("%w: claim carries no one-shot marker reference", ErrInvalidOneShotClaim)
	}
	if strings.TrimSpace(claim.ID) == "" {
		return fmt.Errorf("%w: claim WorkItem ID is required", ErrInvalidOneShotClaim)
	}
	if err := claim.Owner.validate(); err != nil {
		return fmt.Errorf("%w: claim owner is invalid: %v", ErrInvalidOneShotClaim, err)
	}
	if err := claim.OneShot.validate(); err != nil {
		return fmt.Errorf("%w: claim marker reference is invalid: %v", ErrInvalidOneShotClaim, err)
	}
	persistedRef, err := parseOneShotRef(claim.InputSnapshot)
	if err != nil {
		return fmt.Errorf("%w: claim input snapshot is invalid: %v", ErrInvalidOneShotClaim, err)
	}
	if persistedRef == nil || *persistedRef != *claim.OneShot {
		return fmt.Errorf("%w: claim input snapshot marker reference does not match claim", ErrInvalidOneShotClaim)
	}
	return nil
}

func validateSettlementMarkerState(row oneShotMarkerRow, claimedEpoch int64) error {
	switch row.State {
	case "running", "cleanup_blocked":
		if row.Epoch != claimedEpoch {
			return ErrOneShotReclaiming
		}
		return nil
	case "reclaim_pending", "reclaiming", "cleaned":
		return ErrOneShotReclaiming
	default:
		return fmt.Errorf("%w: unsupported one-shot marker state %q", ErrInvalidOneShotLedger, row.State)
	}
}

func validatePersistedOneShotLease(claim Claim, lease AdmissionLease, entry oneShotLedgerEntry) error {
	if err := validateOneShotClaimContext(claim); err != nil {
		return err
	}
	if err := validateAdmissionLeaseShape(lease); err != nil {
		return err
	}
	persistedAt, err := parseCanonicalOneShotTimestamp(entry.AdmittedAt)
	if err != nil {
		return fmt.Errorf("%w: persisted admission timestamp is invalid", ErrInvalidOneShotLedger)
	}
	if lease.AdmittedAt.UTC().Format(time.RFC3339Nano) != persistedAt.Format(time.RFC3339Nano) {
		return fmt.Errorf("%w: supplied admission timestamp does not match persisted lease", ErrInvalidOneShotLedger)
	}
	if lease.MarkerID != claim.OneShot.MarkerID ||
		lease.AdmissionID != entry.AdmissionID ||
		lease.WorkItemID != claim.ID || lease.WorkItemID != entry.WorkItemID ||
		lease.ClaimedEpoch != claim.OneShot.AdmissionEpoch || lease.ClaimedEpoch != entry.ClaimedEpoch ||
		lease.OwnerType != claim.Owner.Type || lease.OwnerType != entry.OwnerType ||
		lease.OwnerID != claim.Owner.ID || lease.OwnerID != entry.OwnerID {
		return fmt.Errorf("%w: persisted one-shot lease identity does not match claim", ErrInvalidOneShotLedger)
	}
	return nil
}

// lockOneShotSettlementTx locks and validates the marker before a one-shot
// finish/retry may lock its WorkItem. Settlement is valid for running and for
// terminal cleanup_blocked (which may retain outstanding leases); reclaiming,
// cleaned, reclaim_pending, stale epochs, and invalid marker data fail closed.
// A missing marker or an already-consumed admission is an idempotent no-op.
func lockOneShotSettlementTx(ctx context.Context, tx *sql.Tx, claim Claim, lease AdmissionLease) (oneShotMarkerRow, bool, error) {
	if err := validateClaimLeaseIdentity(claim, lease); err != nil {
		return oneShotMarkerRow{}, false, err
	}
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, claim.OneShot.MarkerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return oneShotMarkerRow{}, false, nil
	}
	if err != nil {
		return oneShotMarkerRow{}, false, err
	}
	if err := validateSettlementMarkerState(row, claim.OneShot.AdmissionEpoch); err != nil {
		return oneShotMarkerRow{}, false, err
	}
	if err := validateOneShotMarkerLedger(row); err != nil {
		return oneShotMarkerRow{}, false, err
	}
	entry, found, err := findOneShotLease(row, lease.AdmissionID)
	if err != nil {
		return oneShotMarkerRow{}, false, err
	}
	if !found {
		return row, false, nil
	}
	if err := validatePersistedOneShotLease(claim, lease, entry); err != nil {
		return oneShotMarkerRow{}, false, err
	}
	return row, true, nil
}

func settleOneShotLeaseLockedTx(ctx context.Context, tx *sql.Tx, markerID string, row oneShotMarkerRow, admissionID string) (bool, error) {
	entries := append([]oneShotLedgerEntry(nil), row.Leases...)
	for index, entry := range entries {
		if entry.AdmissionID != admissionID {
			continue
		}
		entries = append(entries[:index], entries[index+1:]...)
		if err := writeOneShotLedgerTx(ctx, tx, markerID, entries); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// lockOneShotMarkerTx locks the marker row inside the caller's transaction and
// enforces the running+epoch gate. A missing marker is ErrOneShotMarkerMissing;
// a non-running marker or stale epoch is ErrOneShotReclaiming.
func lockOneShotMarkerTx(ctx context.Context, tx *sql.Tx, markerID string, claimedEpoch int64) (oneShotMarkerRow, error) {
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if err != nil {
		return oneShotMarkerRow{}, err
	}
	if row.State != "running" || row.Epoch != claimedEpoch {
		return row, ErrOneShotReclaiming
	}
	return row, nil
}

// ValidateOneShotMarkerTx locks and validates a one-shot marker for the
// caller's transaction. The SELECT FOR UPDATE row lock is retained until the
// transaction ends, so subsequent WorkItem and owner operations can safely
// follow the marker-first lock order without a separate admission window.
func ValidateOneShotMarkerTx(ctx context.Context, scope txscope.Tx, ref OneShotRef) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("one-shot marker validation requires a transaction")
	}
	if err := ref.validate(); err != nil {
		return err
	}
	_, err := lockOneShotMarkerTx(ctx, tx, ref.MarkerID, ref.AdmissionEpoch)
	return err
}

// AdmitOneShotEffect runs the pre-effect admission gate in its own
// transaction, AFTER the claim transaction committed and released its WorkItem
// lock. It locks the marker row, requires state='running' and the claim's
// current admission epoch, then appends exactly one unique admission lease per
// (work_item_id, claimed epoch): an existing unconsumed lease for the same
// pair is replayed unchanged (crash between gate commit and external call), a
// new pair appends one entry and increments active_effect_count. The gate
// mutates no owner or WorkItem row. A non-running marker or stale epoch
// returns ErrOneShotReclaiming with zero writes.
func (s *Store) AdmitOneShotEffect(ctx context.Context, claim Claim) (AdmissionLease, error) {
	if err := validateOneShotClaimContext(claim); err != nil {
		return AdmissionLease{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AdmissionLease{}, fmt.Errorf("begin one-shot admission gate: %w", err)
	}
	defer tx.Rollback()

	row, err := lockOneShotMarkerTx(ctx, tx, claim.OneShot.MarkerID, claim.OneShot.AdmissionEpoch)
	if err != nil {
		return AdmissionLease{}, err
	}
	if err := validateOneShotMarkerLedger(row); err != nil {
		return AdmissionLease{}, err
	}
	// Marker first, then current WorkItem fence. Cancellation cannot append
	// a fresh effect lease or reuse an old lease as authority for a new effect.
	if err := lockClaim(ctx, tx, claim); err != nil {
		return AdmissionLease{}, err
	}
	// Lease dedup under the marker lock: at most one unconsumed lease per
	// (work_item_id, claimed admission_epoch). Unique random admission ids
	// alone would not prevent double append. Replay is valid only when the
	// complete persisted identity matches this claim; malformed or mismatched
	// evidence fails closed rather than falling back to a new timestamp.
	for _, entry := range row.Leases {
		if entry.WorkItemID != claim.ID || entry.ClaimedEpoch != claim.OneShot.AdmissionEpoch {
			continue
		}
		if entry.OwnerType != claim.Owner.Type || entry.OwnerID != claim.Owner.ID {
			return AdmissionLease{}, fmt.Errorf("%w: persisted admission owner does not match claim", ErrInvalidOneShotLedger)
		}
		lease, leaseErr := leaseEntryToAdmissionLease(claim.OneShot.MarkerID, entry)
		if leaseErr != nil {
			return AdmissionLease{}, leaseErr
		}
		return lease, nil
	}
	now := time.Now().UTC()
	lease := AdmissionLease{
		MarkerID:     claim.OneShot.MarkerID,
		AdmissionID:  uuid.NewString(),
		WorkItemID:   claim.ID,
		OwnerType:    claim.Owner.Type,
		OwnerID:      claim.Owner.ID,
		ClaimedEpoch: claim.OneShot.AdmissionEpoch,
		AdmittedAt:   now,
	}
	row.Leases = append(row.Leases, oneShotLedgerEntry{
		AdmissionID:  lease.AdmissionID,
		WorkItemID:   lease.WorkItemID,
		OwnerType:    lease.OwnerType,
		OwnerID:      lease.OwnerID,
		ClaimedEpoch: lease.ClaimedEpoch,
		AdmittedAt:   now.Format(time.RFC3339Nano),
	})
	if err := writeOneShotLedgerTx(ctx, tx, claim.OneShot.MarkerID, row.Leases); err != nil {
		return AdmissionLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdmissionLease{}, fmt.Errorf("commit one-shot admission gate: %w", err)
	}
	s.logEvent(claim.Owner, claim.ID, "oneshot_admit", "one_shot_effect_admitted",
		fmt.Sprintf("marker=%s epoch=%d admission_count=%d", hashToken(claim.OneShot.MarkerID), claim.OneShot.AdmissionEpoch, len(row.Leases)), claim.Attempt)
	return lease, nil
}

// ResumeRefusedOneShotClaim handles a claim the gate refused with
// ErrOneShotReclaiming. When its marker is cleanup_blocked at the claim's
// epoch and the chain is still open (blocked while its work was live), the
// marker returns to running and the claim returns to available without using
// an attempt, in one transaction (migration 104); the next claim is admitted.
// Any other refusal is left untouched and reported false: the claim keeps
// the non-mutating abort and its lease expires.
func (s *Store) ResumeRefusedOneShotClaim(ctx context.Context, claim Claim) (bool, error) {
	if err := validateOneShotClaimContext(claim); err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin one-shot claim resume: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, claim.OneShot.MarkerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.State != OneShotStateCleanupBlocked || row.Epoch != claim.OneShot.AdmissionEpoch {
		return false, nil
	}
	// The anchored graph must still resolve: a marker blocked on corrupt
	// anchors stays blocked, so its refusals keep the ordinary abort.
	graph, graphErr := (&OneShotReclaimService{}).loadAnchorsTx(ctx, tx, claim.OneShot.MarkerID)
	if graphErr != nil || graph.downloadTaskID == "" {
		return false, nil
	}
	reply, err := resumeBlockedOneShotChainTx(ctx, tx, claim.OneShot.MarkerID, &claim)
	if err != nil {
		return false, err
	}
	if !reply.Resumed {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit one-shot claim resume: %w", err)
	}
	s.logEvent(claim.Owner, claim.ID, "oneshot_resume", "one_shot_resumed",
		fmt.Sprintf("marker=%s epoch=%d plans=%d refunded_attempts=%d", hashToken(claim.OneShot.MarkerID), claim.OneShot.AdmissionEpoch, reply.Plans, reply.Refunded), claim.Attempt)
	return true, nil
}

// writeOneShotLedgerTx persists the ledger array and synchronized count. The
// caller already holds the marker row lock.
func writeOneShotLedgerTx(ctx context.Context, tx *sql.Tx, markerID string, entries []oneShotLedgerEntry) error {
	if err := validateOneShotLedger(int32(len(entries)), entries); err != nil {
		return err
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode one-shot admission ledger: %w", err)
	}
	result, err := persistence.New(tx).WriteOneShotAdmissionLedger(ctx, persistence.WriteOneShotAdmissionLedgerParams{
		ActiveEffectLeases: raw,
		ActiveEffectCount:  int32(len(entries)),
		MarkerID:           markerID,
	})
	if err != nil {
		return fmt.Errorf("update one-shot admission ledger: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read one-shot admission ledger result: %w", err)
	}
	if affected != 1 {
		return ErrOneShotMarkerMissing
	}
	return nil
}

// reassignOneShotLeaseTx continues an ambiguous admission across a manual
// retry. The marker row is already locked by the caller, so this helper keeps
// the original admission id and every other piece of ledger evidence while
// changing only the WorkItem reference. A missing match is deliberately a
// no-op: a manual retry without an outstanding ambiguous admission still gets
// an ordinary fresh gate on its replacement item.
func reassignOneShotLeaseTx(ctx context.Context, tx *sql.Tx, markerID string, marker oneShotMarkerRow, oldWorkItemID, replacementWorkItemID string, expectedOwner OwnerRef, claimedEpoch int64) error {
	if tx == nil {
		return fmt.Errorf("%w: reassign requires a transaction", ErrInvalidOneShotLedger)
	}
	if strings.TrimSpace(markerID) == "" || strings.TrimSpace(oldWorkItemID) == "" || strings.TrimSpace(replacementWorkItemID) == "" || claimedEpoch < 1 {
		return fmt.Errorf("%w: invalid reassign identity", ErrInvalidOneShotLedger)
	}
	if err := expectedOwner.validate(); err != nil {
		return fmt.Errorf("%w: expected owner is invalid: %v", ErrInvalidOneShotLedger, err)
	}
	if marker.Epoch != claimedEpoch {
		return fmt.Errorf("%w: marker epoch %d does not match claimed epoch %d", ErrInvalidOneShotLedger, marker.Epoch, claimedEpoch)
	}
	if err := validateOneShotLedger(int32(len(marker.Leases)), marker.Leases); err != nil {
		return err
	}
	entries := append([]oneShotLedgerEntry(nil), marker.Leases...)
	matchIndex := -1
	for i := range entries {
		if entries[i].WorkItemID == oldWorkItemID && entries[i].ClaimedEpoch == claimedEpoch {
			if matchIndex >= 0 {
				return fmt.Errorf("%w: multiple admission leases match work item %s at epoch %d", ErrInvalidOneShotLedger, oldWorkItemID, claimedEpoch)
			}
			matchIndex = i
		}
	}
	if matchIndex < 0 {
		return nil
	}
	entry := entries[matchIndex]
	identity, err := persistence.New(tx).LockOneShotWorkItemIdentity(ctx, oldWorkItemID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: old one-shot WorkItem %s was not found", ErrInvalidOneShotLedger, oldWorkItemID)
	}
	if err != nil {
		return fmt.Errorf("%w: lock old one-shot WorkItem: %v", ErrInvalidOneShotLedger, err)
	}
	if identity.ID != oldWorkItemID || identity.OwnerType != expectedOwner.Type || identity.OwnerID != expectedOwner.ID ||
		entry.OwnerType != expectedOwner.Type || entry.OwnerID != expectedOwner.ID {
		return fmt.Errorf("%w: persisted WorkItem identity does not match expected owner", ErrInvalidOneShotLedger)
	}
	ref, err := parseOneShotRef(identity.InputSnapshot)
	if err != nil {
		return fmt.Errorf("%w: parse persisted one-shot reference: %v", ErrInvalidOneShotLedger, err)
	}
	if err := validateOneShotOwnerContext(markerID, entry, expectedOwner, ref, claimedEpoch); err != nil {
		return err
	}
	entries[matchIndex].WorkItemID = replacementWorkItemID
	if err := validateOneShotLedger(int32(len(entries)), entries); err != nil {
		return err
	}
	return writeOneShotLedgerTx(ctx, tx, markerID, entries)
}

// dispositionConsumesLease reports whether the typed disposition settles the
// admission lease. Ambiguous outcomes keep the lease: the marker/owner remain
// retained until lookup or recovery settles the disposition.
func dispositionConsumesLease(disposition EffectDisposition) bool {
	switch disposition {
	case DispositionConfirmed, DispositionNoExternalEffect:
		return true
	default:
		return false
	}
}

// FinishOneShotWithResult commits the marker-first settlement and the fenced
// terminal owner/WorkItem transition in ONE transaction. Order: lock marker ->
// settle lease (confirmed/no_external_effect only; ambiguous keeps it) -> lock
// WorkItem under the fencing token -> terminalize WorkItem -> write owner ->
// patch the fenced owner version. Terminalizing before the owner write frees
// the live (available|leased) unique slot so a writer creating a same-owner
// same-kind continuation (one-shot qB sync progress) inserts a fresh item
// instead of merging into the dying leased one; the single transaction keeps
// the whole transition atomic either way.
//
// A stale fence still allows the marker-only settlement to commit (the
// external disposition is durable evidence) while zero owner/WorkItem writes
// happen; ErrStaleClaim is returned so the caller knows the fenced half did
// not apply. An already-consumed lease is a typed no-op: neither owner nor
// WorkItem may change after another path settled the admission.
func (s *Store) FinishOneShotWithResult(ctx context.Context, claim Claim, lease AdmissionLease, disposition EffectDisposition, terminalState string, ownerResult OwnerResult, writeOwner OwnerWriter) error {
	return s.finishOneShotWithResult(ctx, claim, lease, disposition, terminalState, ownerResult, writeOwner, nil)
}

// FinishOneShotWithResultPostTransition is the post-transition variant for
// one-shot claims whose completion must durably create a follow-up row. The
// hook runs in the SAME transaction, after the WorkItem terminal transition and
// the owner write and before commit, so a crash cannot lose the follow-up while
// the owner effect is already committed. It mirrors
// FinishWithResultPostTransition for the marker-first settlement path.
func (s *Store) FinishOneShotWithResultPostTransition(ctx context.Context, claim Claim, lease AdmissionLease, disposition EffectDisposition, terminalState string, ownerResult OwnerResult, writeOwner OwnerWriter, postTransition func(context.Context, txscope.Tx) error) error {
	if postTransition == nil {
		return fmt.Errorf("%w: post-transition hook is required", ErrInvalidSpec)
	}
	return s.finishOneShotWithResult(ctx, claim, lease, disposition, terminalState, ownerResult, writeOwner, postTransition)
}

func (s *Store) finishOneShotWithResult(ctx context.Context, claim Claim, lease AdmissionLease, disposition EffectDisposition, terminalState string, ownerResult OwnerResult, writeOwner OwnerWriter, postTransition func(context.Context, txscope.Tx) error) error {
	if err := validateClaimLeaseIdentity(claim, lease); err != nil {
		return err
	}
	if terminalState != StateSucceeded && terminalState != StateFailed {
		return fmt.Errorf("%w: terminal state must be succeeded or failed", ErrInvalidSpec)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin one-shot finish transaction: %w", err)
	}
	defer tx.Rollback()

	// Marker-first: lock and validate the marker before touching the WorkItem.
	// This also permits settlement of a cleanup_blocked marker while retaining
	// the lease, but never permits reclaiming, cleaned, or invalid states.
	marker, found, err := lockOneShotSettlementTx(ctx, tx, claim, lease)
	if err != nil {
		return err
	}
	if !found {
		// Missing marker or already-consumed lease: typed idempotent no-op without
		// any owner or WorkItem mutation.
		return tx.Commit()
	}
	consumes := dispositionConsumesLease(disposition)
	if consumes {
		if _, err := settleOneShotLeaseLockedTx(ctx, tx, claim.OneShot.MarkerID, marker, lease.AdmissionID); err != nil {
			return err
		}
	}
	if err := lockClaim(ctx, tx, claim); err != nil {
		if errors.Is(err, ErrStaleClaim) && consumes {
			if commitErr := tx.Commit(); commitErr != nil {
				return fmt.Errorf("commit one-shot marker-only settlement: %w", commitErr)
			}
		}
		return err
	}
	// Terminalize before the owner write: a same-owner same-kind continuation
	// created by the writer must not merge into this dying leased item.
	if err := persistence.New(tx).FinishOneShotWorkItem(ctx, persistence.FinishOneShotWorkItemParams{
		State: terminalState,
		ID:    claim.ID,
	}); err != nil {
		return fmt.Errorf("finish one-shot work item: %w", err)
	}
	ownerVersion := claim.ExpectedOwnerVersion
	if writeOwner != nil {
		ownerVersion, err = writeOwner(ctx, txscope.Wrap(tx), ownerResult)
		if err != nil {
			return fmt.Errorf("write one-shot owner result: %w", err)
		}
	}
	if err := persistence.New(tx).PatchOneShotWorkItemOwnerVersion(ctx, persistence.PatchOneShotWorkItemOwnerVersionParams{
		ExpectedOwnerVersion: ownerVersion,
		ID:                   claim.ID,
	}); err != nil {
		return fmt.Errorf("fence one-shot work item owner version: %w", err)
	}
	if postTransition != nil {
		if err := postTransition(ctx, txscope.Wrap(tx)); err != nil {
			return fmt.Errorf("run one-shot finish post-transition: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit one-shot finish transaction: %w", err)
	}
	s.logEvent(claim.Owner, claim.ID, "finish", "work_item_finished",
		fmt.Sprintf("state_after=%s disposition=%s attempt=%d fencing_token=%d", terminalState, disposition, claim.Attempt, claim.FencingToken), claim.Attempt)
	return nil
}

// RetryOneShot mirrors Retry with marker-first settlement: lock marker ->
// settle lease when the disposition consumes it -> lock WorkItem under the
// fencing token -> advance the owner retry state/reason/version -> return the
// same item to available with a database-time future available_at (or mark it
// terminally failed at the attempt budget). Attempt is never incremented here.
// A stale fence commits the marker-only settlement and returns ErrStaleClaim
// with zero owner/WorkItem writes; an already-consumed lease is a typed no-op.
func (s *Store) RetryOneShot(ctx context.Context, claim Claim, lease AdmissionLease, disposition EffectDisposition, reasonCode, reason string, delay time.Duration, writeOwner OwnerWriter) (RetryOutcome, error) {
	if err := validateClaimLeaseIdentity(claim, lease); err != nil {
		return RetryOutcome{}, err
	}
	if len(reasonCode) == 0 || len(reasonCode) > 128 {
		return RetryOutcome{}, fmt.Errorf("%w: reason code is required and bounded", ErrInvalidSpec)
	}
	if len(reason) > 2048 {
		return RetryOutcome{}, fmt.Errorf("%w: reason is outside the allowed bounds", ErrInvalidSpec)
	}
	if delay < 0 || delay > 24*time.Hour {
		return RetryOutcome{}, fmt.Errorf("%w: retry delay is outside the allowed bounds", ErrInvalidSpec)
	}
	terminal := claim.Attempt >= claim.MaxAttempts
	ownerState := "waiting_retry"
	if terminal {
		ownerState = StateFailed
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RetryOutcome{}, fmt.Errorf("begin one-shot retry transaction: %w", err)
	}
	defer tx.Rollback()

	// Marker-first: validate and retain the marker lock before any WorkItem
	// lock, including ambiguous dispositions. Ambiguous keeps its lease, but a
	// cleanup_blocked marker is allowed to proceed only with a valid lease.
	marker, found, err := lockOneShotSettlementTx(ctx, tx, claim, lease)
	if err != nil {
		return RetryOutcome{}, err
	}
	if !found {
		return RetryOutcome{}, tx.Commit()
	}
	consumes := dispositionConsumesLease(disposition)
	if consumes {
		if _, err := settleOneShotLeaseLockedTx(ctx, tx, claim.OneShot.MarkerID, marker, lease.AdmissionID); err != nil {
			return RetryOutcome{}, err
		}
	}
	if err := lockClaim(ctx, tx, claim); err != nil {
		if errors.Is(err, ErrStaleClaim) && consumes {
			if commitErr := tx.Commit(); commitErr != nil {
				return RetryOutcome{}, fmt.Errorf("commit one-shot marker-only settlement: %w", commitErr)
			}
		}
		return RetryOutcome{}, err
	}
	if terminal && !consumes {
		// Unknown effects are not ordinary budget exhaustion. Keep marker
		// ledger and owner untouched, quarantine business execution instead.
		if _, err := persistence.New(tx).BlockWorkItemRecovery(ctx, persistence.BlockWorkItemRecoveryParams{ID: claim.ID, FencingToken: claim.FencingToken, Reason: "effect_unknown"}); err != nil {
			return RetryOutcome{}, err
		}
		return RetryOutcome{}, tx.Commit()
	}
	var currentExpectedVersion int64
	if currentExpectedVersion, err = persistence.New(tx).ReadCurrentWorkItemOwnerVersion(ctx, claim.ID); err != nil {
		return RetryOutcome{}, fmt.Errorf("read current one-shot work item owner version: %w", err)
	}
	if writeOwner == nil && currentExpectedVersion != claim.ExpectedOwnerVersion {
		return RetryOutcome{}, ErrStaleClaim
	}
	ownerVersion := claim.ExpectedOwnerVersion
	if writeOwner != nil {
		ownerVersion, err = writeOwner(ctx, txscope.Wrap(tx), OwnerResult{State: ownerState, ReasonCode: reasonCode, Reason: reason})
		if err != nil {
			return RetryOutcome{}, fmt.Errorf("write one-shot owner retry result: %w", err)
		}
		if ownerVersion < currentExpectedVersion {
			return RetryOutcome{}, ErrStaleClaim
		}
	}
	if terminal {
		result, execErr := persistence.New(tx).FailWorkItemAtAttemptBudget(ctx, persistence.FailWorkItemAtAttemptBudgetParams{
			OwnerVersion:           ownerVersion,
			ReasonCode:             reasonCode,
			Reason:                 reason,
			ID:                     claim.ID,
			CurrentExpectedVersion: currentExpectedVersion,
		})
		if execErr != nil {
			return RetryOutcome{}, fmt.Errorf("fail one-shot work item at attempt budget: %w", execErr)
		}
		if result != 1 {
			return RetryOutcome{}, ErrStaleClaim
		}
	} else {
		result, execErr := persistence.New(tx).ReturnWorkItemToAvailable(ctx, persistence.ReturnWorkItemToAvailableParams{
			DelaySeconds:           delay.Seconds(),
			OwnerVersion:           ownerVersion,
			ReasonCode:             reasonCode,
			Reason:                 reason,
			ID:                     claim.ID,
			CurrentExpectedVersion: currentExpectedVersion,
		})
		if execErr != nil {
			return RetryOutcome{}, fmt.Errorf("return one-shot work item to available: %w", execErr)
		}
		if result != 1 {
			return RetryOutcome{}, ErrStaleClaim
		}
	}
	if err := tx.Commit(); err != nil {
		return RetryOutcome{}, fmt.Errorf("commit one-shot retry transaction: %w", err)
	}
	stateAfter := StateAvailable
	operation := "retry"
	if terminal {
		stateAfter = StateFailed
		operation = "retry_exhausted"
	}
	s.logEvent(claim.Owner, claim.ID, operation, "work_item_retried",
		fmt.Sprintf("state_after=%s disposition=%s reason_code=%s attempt=%d max_attempts=%d fencing_token=%d",
			stateAfter, disposition, reasonCode, claim.Attempt, claim.MaxAttempts, claim.FencingToken), claim.Attempt)
	return RetryOutcome{Terminal: terminal}, nil
}

// CreateOrMergeOneShot inserts or merges a downstream one-shot WorkItem inside
// the caller's upstream owner-transition transaction under the marker-first
// lock order: lock the marker, require state='running' and the current epoch,
// then complete the insertion in the same transaction. A non-running marker or
// stale epoch returns ErrOneShotReclaiming with zero inserts and zero owner
// mutation. There is no autonomous one-shot dispatcher, so every re-creatable
// WorkItem is auditable against this marker-locked transition.
func (s *Store) CreateOrMergeOneShot(ctx context.Context, scope txscope.Tx, ref OneShotRef, spec WorkItemSpec) (id string, created bool, err error) {
	tx := scope.SQLTx()
	if tx == nil {
		return "", false, errors.New("one-shot create-or-merge requires a transaction")
	}
	if err := ref.validate(); err != nil {
		return "", false, err
	}
	if _, err := lockOneShotMarkerTx(ctx, tx, ref.MarkerID, ref.AdmissionEpoch); err != nil {
		return "", false, err
	}
	spec.OneShot = &ref
	return s.CreateOrMerge(ctx, tx, spec)
}

// CreateOneShot inserts a fresh one-shot WorkItem inside the caller's
// transaction under the same marker-first lock order as CreateOrMergeOneShot,
// but never merges into an existing live item: a second live item for the same
// owner and kind stays the typed ErrLiveItemConflict. Manual retry of a
// one-shot item must use this gate so a reclaiming or cleaned marker produces
// zero inserts while a running marker admits exactly the declared reference.
func (s *Store) CreateOneShot(ctx context.Context, scope txscope.Tx, ref OneShotRef, spec WorkItemSpec) (string, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return "", errors.New("one-shot create requires a transaction")
	}
	if err := ref.validate(); err != nil {
		return "", err
	}
	marker, err := lockOneShotMarkerTx(ctx, tx, ref.MarkerID, ref.AdmissionEpoch)
	if err != nil {
		return "", err
	}
	return s.createOneShotMarkerLocked(ctx, tx, marker, ref, spec)
}

// createOneShotMarkerLocked inserts a one-shot WorkItem after the caller has
// already locked and validated its marker. It deliberately does not acquire
// the marker lock again: marker-first callers such as manual retry must keep
// the marker lock while they lock the failed WorkItem, reset the owner, and
// insert the replacement row.
func (s *Store) createOneShotMarkerLocked(ctx context.Context, tx *sql.Tx, marker oneShotMarkerRow, ref OneShotRef, spec WorkItemSpec) (string, error) {
	if tx == nil {
		return "", errors.New("one-shot create requires a transaction")
	}
	if err := ref.validate(); err != nil {
		return "", err
	}
	if marker.State != "running" || marker.Epoch != ref.AdmissionEpoch {
		return "", ErrOneShotReclaiming
	}
	spec.OneShot = &ref
	return s.Create(ctx, tx, spec)
}

// MergeOneShotInput injects the canonical one_shot object into an input
// snapshot using the store's shared JSON semantics. Upstream owners use this
// when rebuilding a one-shot snapshot so they never duplicate the one_shot
// object shape.
func MergeOneShotInput(snapshot json.RawMessage, ref OneShotRef) (json.RawMessage, error) {
	return mergeOneShotSnapshot(snapshot, &ref)
}

// hashToken redacts an opaque identifier for structured operator logs.
func hashToken(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}
