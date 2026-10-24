package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/anime/animeconfig"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/nativeinput"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// Exercise the fixed production constructor with a real owner chain and a real
// torrent, inside the existing disposable fixture transaction. No golden input
// is supplied to the adapter; rollback preserves the other historical checks.
func checkEntryAdaptersPostgres(t *testing.T, ctx context.Context, tx *sql.Tx, db *sql.DB, revisions *draft.Service, version int64) {
	t.Helper()
	force, err := NewManualRetryAcceptor(db, revisions)
	if err != nil {
		t.Fatal(err)
	}
	scope := txscope.Wrap(tx)
	if _, err := force.Accept(ctx, scope, PreparedHandle{}); err == nil {
		t.Fatal("zero prepared consumed")
	}
	if _, err := force.Committed(PendingHandle{}); err == nil {
		t.Fatal("zero pending committed")
	}
	// This section accepts a release, which writes an acquisition, a download
	// task and the release's own task reference. The savepoint scopes those
	// writes back out so the historical checks that follow, which read that
	// release inside the same fixture transaction, still see it unaccepted.
	if _, err := tx.ExecContext(ctx, `SAVEPOINT adapter_projection`); err != nil {
		t.Fatal(err)
	}
	resolved, err := animeconfig.LoadResolved(ctx, tx, "anime-1")
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewOwnerResolution(resolved)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := nativeinput.OwnerPolicy(resolved)
	if err != nil {
		t.Fatal(err)
	}
	input := EntryTask{ReleaseID: "rel-1", ExpectedVersion: version, Attempt: 2, FrozenInput: json.RawMessage(`{}`), FrozenPolicy: policy, Resolution: resolution, MaxAttempts: 5}
	p, err := force.Prepare(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	if p.Replayed() {
		t.Fatal("fresh replay")
	}
	v, err := protocol.ParseStrict([]byte(p.prepared.args.InputCanonicalUtf8.String))
	if err != nil {
		t.Fatal(err)
	}
	route := v.Object["delivery"].Object["route"].Object
	for key, reason := range map[string]string{"root": "anime_root_unavailable", "path_mapping": "path_mapping_unconfigured"} {
		if route[key].Object["state"].String != "unavailable" || route[key].Object["reason_code"].String != reason {
			t.Fatalf("%s unavailable not frozen", key)
		}
	}
	if v.Object["schema_version"].Int != 1 {
		t.Fatal("production did not create the flattened input schema")
	}
	automatic, err := NewRSSAutomaticAcceptor(db, revisions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := automatic.Accept(ctx, scope, p); err == nil {
		t.Fatal("foreign prepared consumed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id='rel-1'`); err != nil {
		t.Fatal(err)
	}
	pending, err := force.Accept(ctx, scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if pending.TaskID() == "" {
		t.Fatal("pending task missing")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions WHERE input_schema_version=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("adapter root missing")
	}
	replay, err := force.Prepare(ctx, scope, input)
	if err != nil || !replay.Replayed() {
		t.Fatalf("exact replay: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT adapter_projection; RELEASE SAVEPOINT adapter_projection`); err != nil {
		t.Fatal(err)
	}
}
