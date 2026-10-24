package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/acquisition/terminal/settlement"
)

// A typed file failure records its own lower-case code (target_parent_invalid
// here, from a target parent that is a regular file). Settlement must map it
// onto the receipt grammar and end the root failed; reading it raw made the
// settler report EXECUTION_GRAPH_INCOMPLETE and left the root running forever.
func TestSettlementEndsRootOnLowercaseFileFailureCode(t *testing.T) {
	ctx := context.Background()
	x := newTypedRuntimeFixture(t, nil, false)
	parent := filepath.Dir(x.target)
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed failure run: handled=%v err=%v", handled, err)
	}
	if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != "target_parent_invalid" {
		t.Fatalf("operation = %+v, want failed with target_parent_invalid", got)
	}
	root := acquisitionOf(t, x.db, x.opID)
	settler, err := settlement.NewSettlementService(x.db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle root: status=%q err=%v", status, err)
	}
	var state, reason string
	var blocked *string
	if err := x.db.QueryRow(`SELECT state, terminal_receipt->'primary_failure'->>'reason_code', settlement_blocked_code FROM acquisitions WHERE id=$1`, root).
		Scan(&state, &reason, &blocked); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || reason != "TARGET_PARENT_INVALID" || blocked != nil {
		t.Fatalf("settled root state=%q reason=%q blocked=%v, want failed/TARGET_PARENT_INVALID/none", state, reason, blocked)
	}
}
