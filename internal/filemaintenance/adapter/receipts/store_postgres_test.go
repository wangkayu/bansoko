package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/disposabledb/guard"
)

// jsonb normalizes whitespace on read back, so payloads compare by value.
func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

// TestStoreReceiptLifecycle drives the PostgreSQL ReceiptStore over the real
// table under the runtime role: the start proof exists once per token, the
// final receipt completes exactly one open row and is never discarded, a
// refused invocation's proof is removed, and the invocation lock is a
// non-blocking session advisory lock.
func TestStoreReceiptLifecycle(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	runtimeDB := guard.MustRuntime(t)
	defer runtimeDB.Close()
	store := NewStore(runtimeDB)
	ctx := context.Background()

	// Absent tokens answer with the sentinel errors, and a final receipt
	// never lands on a row without a start proof.
	if _, err := store.ReadStarted(ctx, "maintenance-rcpt-absent"); !errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("read absent started = %v", err)
	}
	if _, err := store.ReadFinal(ctx, "maintenance-rcpt-absent"); !errors.Is(err, maintenance.ErrMaintenanceFinalAbsent) {
		t.Fatalf("read absent final = %v", err)
	}
	if err := store.WriteFinal(ctx, "maintenance-rcpt-absent", []byte(`{"kind":"final"}`)); err == nil {
		t.Fatal("final receipt on an unstarted invocation must be refused")
	}

	// The start proof exists once; a second write is refused even before any
	// effect ran, because the recovery path reads that row.
	if err := store.WriteStarted(ctx, "maintenance-rcpt-run", []byte(`{"kind":"start"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteStarted(ctx, "maintenance-rcpt-run", []byte(`{"kind":"start"}`)); err == nil {
		t.Fatal("a second start proof must be refused")
	}
	if proof, err := store.ReadStarted(ctx, "maintenance-rcpt-run"); err != nil || !sameJSON(t, proof, `{"kind":"start"}`) {
		t.Fatalf("read started = %s %v", proof, err)
	}
	if _, err := store.ReadFinal(ctx, "maintenance-rcpt-run"); !errors.Is(err, maintenance.ErrMaintenanceFinalAbsent) {
		t.Fatalf("final before completion = %v", err)
	}

	// The final receipt completes the row exactly once and survives discard.
	if err := store.WriteFinal(ctx, "maintenance-rcpt-run", []byte(`{"kind":"final"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteFinal(ctx, "maintenance-rcpt-run", []byte(`{"kind":"final"}`)); err == nil {
		t.Fatal("a second final receipt must be refused")
	}
	if receipt, err := store.ReadFinal(ctx, "maintenance-rcpt-run"); err != nil || !sameJSON(t, receipt, `{"kind":"final"}`) {
		t.Fatalf("read final = %s %v", receipt, err)
	}
	if err := store.DiscardStarted(ctx, "maintenance-rcpt-run"); err == nil {
		t.Fatal("a finalized receipt row must never be discarded")
	}
	if _, err := store.ReadFinal(ctx, "maintenance-rcpt-run"); err != nil {
		t.Fatalf("final receipt survived the refused discard: %v", err)
	}

	// A refused invocation's start proof is removed; an unknown token is a
	// no-op, not an error.
	if err := store.WriteStarted(ctx, "maintenance-rcpt-refused", []byte(`{"kind":"start"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardStarted(ctx, "maintenance-rcpt-refused"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadStarted(ctx, "maintenance-rcpt-refused"); !errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("discarded start proof = %v", err)
	}
	if err := store.DiscardStarted(ctx, "maintenance-rcpt-never"); err != nil {
		t.Fatalf("discard of an unknown token = %v", err)
	}

	// The invocation lock is held on a dedicated session: a second taker gets
	// ErrMaintenanceLocked instead of queueing, and the token is lockable
	// again once released.
	unlock, err := store.Lock(ctx, "maintenance-rcpt-lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(ctx, "maintenance-rcpt-lock"); !errors.Is(err, maintenance.ErrMaintenanceLocked) {
		t.Fatalf("contended lock = %v", err)
	}
	unlock()
	relock, err := store.Lock(ctx, "maintenance-rcpt-lock")
	if err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	relock()
}
