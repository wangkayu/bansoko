package workitems

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPersistedOneShotLeaseRequiresCompleteIdentity(t *testing.T) {
	ref := OneShotRef{MarkerID: "marker-1", CorrelationID: "corr-1", AdmissionEpoch: 2}
	claim := Claim{
		ID:            "work-item-1",
		Owner:         OwnerRef{Type: OwnerDownloadTask, ID: "task-1"},
		InputSnapshot: json.RawMessage(`{"one_shot":{"marker_id":"marker-1","correlation_id":"corr-1","admission_epoch":2}}`),
		OneShot:       &ref,
	}
	lease := AdmissionLease{
		MarkerID:     ref.MarkerID,
		AdmissionID:  "admission-1",
		WorkItemID:   claim.ID,
		OwnerType:    claim.Owner.Type,
		OwnerID:      claim.Owner.ID,
		ClaimedEpoch: ref.AdmissionEpoch,
		AdmittedAt:   time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	entry := oneShotLedgerEntry{
		AdmissionID: lease.AdmissionID, WorkItemID: lease.WorkItemID,
		OwnerType: lease.OwnerType, OwnerID: lease.OwnerID,
		ClaimedEpoch: lease.ClaimedEpoch, AdmittedAt: lease.AdmittedAt.Format(time.RFC3339Nano),
	}
	if err := validatePersistedOneShotLease(claim, lease, entry); err != nil {
		t.Fatalf("valid complete lease identity rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*AdmissionLease, *oneShotLedgerEntry)
	}{
		{name: "supplied timestamp mismatch", mutate: func(lease *AdmissionLease, _ *oneShotLedgerEntry) {
			lease.AdmittedAt = lease.AdmittedAt.Add(time.Second)
		}},
		{name: "persisted timestamp mismatch", mutate: func(_ *AdmissionLease, entry *oneShotLedgerEntry) {
			entry.AdmittedAt = "2026-08-25T00:00:01Z"
		}},
		{name: "admission id mismatch", mutate: func(lease *AdmissionLease, _ *oneShotLedgerEntry) {
			lease.AdmissionID = "other-admission"
		}},
		{name: "owner mismatch", mutate: func(lease *AdmissionLease, _ *oneShotLedgerEntry) {
			lease.OwnerID = "other-owner"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotLease, gotEntry := lease, entry
			test.mutate(&gotLease, &gotEntry)
			if err := validatePersistedOneShotLease(claim, gotLease, gotEntry); err == nil || !errors.Is(err, ErrInvalidOneShotLedger) {
				t.Fatalf("mismatched lease accepted: %v", err)
			}
		})
	}
}

func TestOneShotSettlementMarkerStates(t *testing.T) {
	for _, test := range []struct {
		state string
		want  error
	}{
		{state: "running"},
		{state: "cleanup_blocked"},
		{state: "retention_pending", want: ErrOneShotReclaiming},
		{state: "reclaiming", want: ErrOneShotReclaiming},
		{state: "cleaned", want: ErrOneShotReclaiming},
		{state: "invalid", want: ErrInvalidOneShotLedger},
	} {
		t.Run(test.state, func(t *testing.T) {
			err := validateSettlementMarkerState(oneShotMarkerRow{State: test.state, Epoch: 3}, 3)
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("state validation error = %v, want %v", err, test.want)
			}
			if test.want == nil && err != nil {
				t.Fatalf("allowed state rejected: %v", err)
			}
		})
	}
}
func TestOneShotLedgerValidation(t *testing.T) {
	validEntry := oneShotLedgerEntry{
		AdmissionID: "a-1", WorkItemID: "w-1", OwnerType: OwnerDownloadTask, OwnerID: "task-1",
		ClaimedEpoch: 1, AdmittedAt: "2026-08-25T00:00:00Z",
	}
	cases := []struct {
		name    string
		count   int32
		entries []oneShotLedgerEntry
		wantErr bool
	}{
		{name: "valid count one", count: 1, entries: []oneShotLedgerEntry{validEntry}},
		{name: "valid empty array", count: 0, entries: []oneShotLedgerEntry{}},
		{name: "count cardinality mismatch", count: 0, entries: []oneShotLedgerEntry{validEntry}, wantErr: true},
		{name: "negative count", count: -1, entries: nil, wantErr: true},
		{name: "duplicate admission id", count: 2, entries: []oneShotLedgerEntry{validEntry, validEntry}, wantErr: true},
		{name: "duplicate work item epoch", count: 2, entries: []oneShotLedgerEntry{validEntry, func() oneShotLedgerEntry { e := validEntry; e.AdmissionID = "a-2"; return e }()}, wantErr: true},
		{name: "non canonical timestamp", count: 1, entries: []oneShotLedgerEntry{func() oneShotLedgerEntry { e := validEntry; e.AdmittedAt = "2026-08-25T00:00:00.000Z"; return e }()}, wantErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := validateOneShotLedger(test.count, test.entries)
			if test.wantErr && (err == nil || !errors.Is(err, ErrInvalidOneShotLedger)) {
				t.Fatalf("validation error = %v, want ErrInvalidOneShotLedger", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("valid ledger rejected: %v", err)
			}
		})
	}
}

func TestPersistedOneShotReassignmentIdentity(t *testing.T) {
	ref := OneShotRef{MarkerID: "marker-1", CorrelationID: "corr-1", AdmissionEpoch: 2}
	entry := oneShotLedgerEntry{
		AdmissionID: "admission-1", WorkItemID: "old-work-item", OwnerType: OwnerDownloadTask,
		OwnerID: "task-1", ClaimedEpoch: 2, AdmittedAt: "2026-08-25T00:00:00Z",
	}
	snapshot, err := mergeOneShotSnapshot(json.RawMessage(`{"trigger":"one_shot"}`), &ref)
	if err != nil {
		t.Fatalf("build persisted snapshot: %v", err)
	}
	if parsed, parseErr := parseOneShotRef(snapshot); parseErr != nil || parsed == nil || *parsed != ref {
		t.Fatalf("valid persisted identity did not parse: ref=%+v err=%v", parsed, parseErr)
	}
	for _, test := range []struct {
		name     string
		id       string
		owner    string
		snapshot json.RawMessage
		wantErr  bool
	}{
		{name: "valid", id: "old-work-item", owner: OwnerDownloadTask, snapshot: snapshot},
		{name: "persisted id mismatch", id: "different-work-item", owner: OwnerDownloadTask, snapshot: snapshot, wantErr: true},
		{name: "owner mismatch", id: "old-work-item", owner: OwnerArtifact, snapshot: snapshot, wantErr: true},
		{name: "missing one-shot ref", id: "old-work-item", owner: OwnerDownloadTask, snapshot: json.RawMessage(`{"trigger":"ordinary"}`), wantErr: true},
		{name: "malformed snapshot", id: "old-work-item", owner: OwnerDownloadTask, snapshot: json.RawMessage(`{"one_shot":`), wantErr: true},
		{name: "marker mismatch", id: "old-work-item", owner: OwnerDownloadTask, snapshot: json.RawMessage(`{"one_shot":{"marker_id":"other","correlation_id":"corr-1","admission_epoch":2}}`), wantErr: true},
		{name: "epoch mismatch", id: "old-work-item", owner: OwnerDownloadTask, snapshot: json.RawMessage(`{"one_shot":{"marker_id":"marker-1","correlation_id":"corr-1","admission_epoch":3}}`), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, parseErr := parseOneShotRef(test.snapshot)
			identityErr := parseErr
			if identityErr == nil && (test.id != entry.WorkItemID || test.owner != entry.OwnerType || parsed == nil || parsed.MarkerID != ref.MarkerID || parsed.AdmissionEpoch != entry.ClaimedEpoch) {
				identityErr = ErrInvalidOneShotLedger
			}
			if test.wantErr && (identityErr == nil || !errors.Is(identityErr, ErrInvalidOneShotLedger) && !errors.Is(identityErr, ErrInvalidOneShotClaim)) {
				t.Fatalf("identity validation error = %v, want fail closed", identityErr)
			}
			if !test.wantErr && identityErr != nil {
				t.Fatalf("valid persisted identity rejected: %v", identityErr)
			}
		})
	}
}

func TestOneShotRefValidate(t *testing.T) {
	valid := OneShotRef{MarkerID: "m-1", CorrelationID: "corr-1", AdmissionEpoch: 1}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid ref rejected: %v", err)
	}
	cases := []OneShotRef{
		{MarkerID: "", CorrelationID: "c", AdmissionEpoch: 1},
		{MarkerID: string(make([]byte, 129)), CorrelationID: "c", AdmissionEpoch: 1},
		{MarkerID: "m", CorrelationID: "", AdmissionEpoch: 1},
		{MarkerID: "m", CorrelationID: "c", AdmissionEpoch: 0},
	}
	for index, ref := range cases {
		if err := ref.validate(); err == nil {
			t.Fatalf("case %d: invalid ref accepted", index)
		}
	}
}

func TestParseOneShotRef(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		wantErr  bool
	}{
		{name: "ordinary object", snapshot: `{"trigger":"one_shot_acquire"}`},
		{name: "empty input", snapshot: ""},
		{name: "whitespace input", snapshot: " \t\n"},
		{name: "null top level", snapshot: `null`, wantErr: true},
		{name: "array top level", snapshot: `[1]`, wantErr: true},
		{name: "scalar top level", snapshot: `"snapshot"`, wantErr: true},
		{name: "malformed json", snapshot: `{"one_shot":`, wantErr: true},
		{name: "null marker", snapshot: `{"one_shot":null}`, wantErr: true},
		{name: "scalar marker", snapshot: `{"one_shot":"marker"}`, wantErr: true},
		{name: "array marker", snapshot: `{"one_shot":[]}`, wantErr: true},
		{name: "incomplete marker", snapshot: `{"one_shot":{"marker_id":"m"}}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ref, err := parseOneShotRef(json.RawMessage(test.snapshot))
			if test.wantErr {
				if err == nil || !errors.Is(err, ErrInvalidOneShotClaim) {
					t.Fatalf("parse error = %v, want ErrInvalidOneShotClaim", err)
				}
				return
			}
			if err != nil || ref != nil {
				t.Fatalf("ordinary/empty snapshot must parse to nil ref: ref=%v err=%v", ref, err)
			}
		})
	}
	good := json.RawMessage(`{"trigger":"x","one_shot":{"marker_id":"m","correlation_id":"corr","admission_epoch":2}}`)
	parsed, err := parseOneShotRef(good)
	if err != nil || parsed == nil || parsed.MarkerID != "m" || parsed.AdmissionEpoch != 2 {
		t.Fatalf("good snapshot parse failed: %+v err=%v", parsed, err)
	}
}

func TestParseOneShotRefRejectsTrailingJSON(t *testing.T) {
	for _, snapshot := range []string{
		`{"trigger":"ordinary"} {"another":"value"}`,
		`{"one_shot":{"marker_id":"m","correlation_id":"corr","admission_epoch":1}} true`,
	} {
		if _, err := parseOneShotRef(json.RawMessage(snapshot)); err == nil || !errors.Is(err, ErrInvalidOneShotClaim) {
			t.Fatalf("snapshot %q error = %v, want ErrInvalidOneShotClaim", snapshot, err)
		}
	}
}

func TestMergeOneShotSnapshot(t *testing.T) {
	ref := &OneShotRef{MarkerID: "m-1", CorrelationID: "corr-1", AdmissionEpoch: 3}
	merged, err := mergeOneShotSnapshot(json.RawMessage(`{"trigger":"t"}`), ref)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	parsed, err := parseOneShotRef(merged)
	if err != nil || parsed == nil || *parsed != *ref {
		t.Fatalf("merged snapshot lost the reference: %s -> %+v err=%v", merged, parsed, err)
	}
	// Idempotent merge with the identical reference.
	again, err := mergeOneShotSnapshot(merged, ref)
	if err != nil || string(again) != string(merged) {
		t.Fatalf("idempotent merge changed the snapshot: %s vs %s (err=%v)", again, merged, err)
	}
	// A different reference under the same key is rejected.
	other := &OneShotRef{MarkerID: "m-2", CorrelationID: "corr-1", AdmissionEpoch: 3}
	if _, err := mergeOneShotSnapshot(merged, other); err == nil {
		t.Fatal("conflicting one_shot reference must be rejected")
	}
}

func TestWorkItemSpecOneShotValidation(t *testing.T) {
	base := WorkItemSpec{
		Owner:                OwnerRef{Type: OwnerDownloadTask, ID: "task-1"},
		Kind:                 KindQBSubmit,
		IdempotencyKey:       "effect-key",
		InputSnapshot:        json.RawMessage(`{"trigger":"one_shot_acquire"}`),
		MaxAttempts:          5,
		ExpectedOwnerVersion: 1,
	}
	ref := &OneShotRef{MarkerID: "m-1", CorrelationID: "corr-1", AdmissionEpoch: 1}
	withRef := base
	withRef.OneShot = ref
	if err := withRef.validate(); err != nil {
		t.Fatalf("spec with consistent one-shot ref rejected: %v", err)
	}
	conflict := base
	conflict.OneShot = ref
	conflict.InputSnapshot = json.RawMessage(`{"one_shot":{"marker_id":"other","correlation_id":"corr-1","admission_epoch":1}}`)
	if err := conflict.validate(); err == nil {
		t.Fatal("spec whose snapshot carries a different marker id must be rejected")
	}
	malformed := base
	malformed.OneShot = ref
	malformed.InputSnapshot = json.RawMessage(`{"one_shot":{"marker_id":"m-1"}}`)
	if err := malformed.validate(); err == nil || !errors.Is(err, ErrInvalidOneShotClaim) {
		t.Fatal("spec whose snapshot carries a malformed one_shot object must be rejected")
	}
	ordinaryMarker := base
	ordinaryMarker.InputSnapshot = json.RawMessage(`{"one_shot":{"marker_id":"m-1","correlation_id":"corr-1","admission_epoch":1}}`)
	if err := ordinaryMarker.validate(); err == nil || !errors.Is(err, ErrInvalidOneShotClaim) {
		t.Fatal("ordinary spec carrying a valid one_shot object must be rejected")
	}
}

func TestEffectDispositionConstants(t *testing.T) {
	if DispositionNoExternalEffect != "no_external_effect" ||
		DispositionConfirmed != "confirmed" ||
		DispositionAmbiguous != "ambiguous" {
		t.Fatal("effect disposition constants diverge from the typed contract")
	}
	if !dispositionConsumesLease(DispositionConfirmed) || !dispositionConsumesLease(DispositionNoExternalEffect) {
		t.Fatal("confirmed/no_external_effect must consume the admission lease")
	}
	if dispositionConsumesLease(DispositionAmbiguous) {
		t.Fatal("ambiguous outcomes must keep the lease unconsumed")
	}
}

func TestAdmissionLeaseJSONShape(t *testing.T) {
	lease := AdmissionLease{
		AdmissionID:  "a-1",
		WorkItemID:   "w-1",
		OwnerType:    OwnerDownloadTask,
		OwnerID:      "task-1",
		ClaimedEpoch: 4,
		AdmittedAt:   time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(struct {
		Entry oneShotLedgerEntry `json:"entry"`
	}{Entry: oneShotLedgerEntry{
		AdmissionID: lease.AdmissionID, WorkItemID: lease.WorkItemID,
		OwnerType: lease.OwnerType, OwnerID: lease.OwnerID,
		ClaimedEpoch: lease.ClaimedEpoch, AdmittedAt: lease.AdmittedAt.Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	entry := decoded["entry"]
	for _, key := range []string{"admission_id", "work_item_id", "owner_type", "owner_id", "claimed_admission_epoch", "admitted_at"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("ledger entry JSON is missing key %q: %s", key, raw)
		}
	}
}
