package migrations

import (
	"context"
	"testing"
)

// TestDownloadTaskRequiresAcquisition proves every download task belongs to an
// acquisition root: the column is NOT NULL, references the root, and one root
// owns at most one task. The insert-time budget guard resolves the root before
// any constraint runs, so a task without a usable root is rejected there
// (P0002) and the column rules are asserted through the catalog.
func TestDownloadTaskRequiresAcquisition(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	var notNull bool
	if err := db.QueryRowContext(ctx, `SELECT attnotnull FROM pg_attribute WHERE attrelid='download_tasks'::regclass AND attname='acquisition_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if !notNull {
		t.Fatal("download_tasks.acquisition_id must be NOT NULL")
	}
	for name, kind := range map[string]string{"download_tasks_acquisition_fk": "f", "download_tasks_acquisition_uq": "u"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='download_tasks'::regclass AND conname=$1 AND contype=$2`, name, kind).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("constraint %s (%s) is missing", name, kind)
		}
	}

	seedAcceptableRelease(t, db, "rooted")
	seedAcceptableRelease(t, db, "other")
	if _, err := callQueuedAccept(t, db, validQueued(t, "acq-rooted", "key-rooted", "rooted")); err != nil {
		t.Fatalf("accept a rooted task: %v", err)
	}
	for _, tc := range []struct {
		name, statement, wantState string
	}{
		{"a task without a root", `
INSERT INTO download_tasks(id,release_id,frozen_input,frozen_policy,state,idempotency_key)
VALUES('dt-unrooted','rel-other','{}','{}','queued','dt-unrooted')`, "P0002"},
		{"a task under an unknown root", `
INSERT INTO download_tasks(id,release_id,acquisition_id,frozen_input,frozen_policy,state,idempotency_key)
VALUES('dt-unknown-root','rel-other','acq-missing','{}','{}','queued','dt-unknown-root')`, "P0002"},
		{"a second task for the same root", `
INSERT INTO download_tasks(id,release_id,acquisition_id,frozen_input,frozen_policy,state,idempotency_key)
VALUES('dt-second','rel-other','acq-rooted','{}','{}','queued','dt-second')`, "23505"},
	} {
		if _, err := db.ExecContext(ctx, tc.statement); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		} else if code := sqlState(err); code != tc.wantState {
			t.Errorf("%s: SQLSTATE %q, want %s (%v)", tc.name, code, tc.wantState, err)
		}
	}
}
