package application

import (
	"database/sql"
	"testing"
)

// assertPlacementHandoff: exactly one pending obligation and exactly one live
// placement continuation for the finished operation; the claimed work itself
// is terminal (the continuation is a new item, not the reused claim).
func assertPlacementHandoff(t *testing.T, db *sql.DB, operationID, claimedWork string) {
	t.Helper()
	var obligations, pending int
	if err := db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE state='pending') FROM placement_obligations WHERE file_operation_id=$1`, operationID).Scan(&obligations, &pending); err != nil {
		t.Fatal(err)
	}
	if obligations != 1 || pending != 1 {
		t.Fatalf("placement obligations=%d pending=%d want exactly 1 pending", obligations, pending)
	}
	var live int
	var continuationID string
	if err := db.QueryRow(`SELECT count(*), COALESCE(max(id),'') FROM work_items WHERE owner_id=$1 AND kind='file_execute' AND state IN ('available','leased') AND input_snapshot->>'continuation'='placement'`, operationID).Scan(&live, &continuationID); err != nil {
		t.Fatal(err)
	}
	if live != 1 || continuationID == claimedWork {
		t.Fatalf("placement continuations=%d id=%s claimed=%s want exactly one new live item", live, continuationID, claimedWork)
	}
	var claimedState string
	if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id=$1`, claimedWork).Scan(&claimedState); err != nil || claimedState != "succeeded" {
		t.Fatalf("claimed work state=%s err=%v want succeeded", claimedState, err)
	}
}

func assertPlacementAbsent(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	var obligations, continuations int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM placement_obligations WHERE file_operation_id=$1),
	 (SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND input_snapshot->>'continuation'='placement')`, operationID).Scan(&obligations, &continuations); err != nil {
		t.Fatal(err)
	}
	if obligations != 0 || continuations != 0 {
		t.Fatalf("unexpected placement handoff: obligations=%d continuations=%d", obligations, continuations)
	}
}

// injectPlacementObligationFailure installs an owner-side test trigger that
// fails every obligation insert, to prove the obligation is in the finish
// transaction (its failure must roll back the whole write set).
func injectPlacementObligationFailure(t *testing.T) {
	t.Helper()
	owner := ownerTestDB(t)
	for _, statement := range []string{
		`CREATE FUNCTION public.zz_test_fail_placement_obligation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='TEST_PLACEMENT_OBLIGATION_FAILURE'; END $$`,
		`GRANT EXECUTE ON FUNCTION public.zz_test_fail_placement_obligation() TO PUBLIC`,
		`CREATE TRIGGER zz_test_fail_placement_obligation BEFORE INSERT ON public.placement_obligations FOR EACH ROW EXECUTE FUNCTION public.zz_test_fail_placement_obligation()`,
	} {
		if _, err := owner.Exec(statement); err != nil {
			t.Fatalf("inject placement failure: %v", err)
		}
	}
	t.Cleanup(func() { removePlacementObligationFailure(t) })
}

func removePlacementObligationFailure(t *testing.T) {
	t.Helper()
	owner := ownerTestDB(t)
	if _, err := owner.Exec(`DROP TRIGGER IF EXISTS zz_test_fail_placement_obligation ON public.placement_obligations`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`DROP FUNCTION IF EXISTS public.zz_test_fail_placement_obligation()`); err != nil {
		t.Fatal(err)
	}
}
