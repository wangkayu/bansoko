package migrations

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"bansoko/internal/runtime/database"
)

// These tests prove the shape of the typed effect evidence after the fold: the
// dispatch/time/retry matrix of the submission/publication slot, the bounded
// JSON slots and the absence of cascades plus the runtime privilege split. The
// evidence relations are gone; the slot is a set of columns on the owner row
// (`download_tasks.submit_*` / `file_operations.publication_*`).

// jsonv marks a column value as JSON text so the update casts it.
type jsonv string

// applySlot writes one slot onto an existing owner row. The owner row itself is
// seeded by the accept path or the operation fixture; this only binds the
// prefixed evidence columns, exactly as the owner-held permit functions do.
func applySlot(db execer, table, whereColumn string, whereValue any, row map[string]any) error {
	names := make([]string, 0, len(row))
	for name := range row {
		names = append(names, name)
	}
	sort.Strings(names)
	assignments := make([]string, 0, len(names))
	args := make([]any, 0, len(names))
	for i, name := range names {
		switch v := row[name].(type) {
		case jsonv:
			assignments = append(assignments, fmt.Sprintf("%s=$%d::jsonb", name, i+1))
			args = append(args, string(v))
		case time.Time:
			assignments = append(assignments, fmt.Sprintf("%s=$%d::timestamptz", name, i+1))
			args = append(args, v)
		case nil:
			assignments = append(assignments, fmt.Sprintf("%s=$%d", name, i+1))
			args = append(args, nil)
		default:
			assignments = append(assignments, fmt.Sprintf("%s=$%d", name, i+1))
			args = append(args, v)
		}
	}
	statement := "UPDATE " + table + " SET " + strings.Join(assignments, ",") +
		" WHERE " + whereColumn + "=$" + strconv.Itoa(len(args)+1)
	_, err := db.ExecContext(context.Background(), statement, append(args, whereValue)...)
	return err
}

func merged(base map[string]any, overrides map[string]any) map[string]any {
	return with2(base, overrides)
}

const (
	submissionTable  = "download_tasks"
	publicationTable = "file_operations"
)

// rootEnvTaskID is the download task the queued accept seeds for a root.
func rootEnvTaskID(root string) string { return "dt-q-" + root }

func submissionSlot(permit, root string, now time.Time) map[string]any {
	return map[string]any{
		"submit_permit_id": permit, "submit_work_item_id": "work-" + permit,
		"submit_fencing_token": 1, "submit_owner_version": 1, "submit_effect_key": "key-" + permit,
		"submit_spec_digest": acqDigest("a"), "submit_path_snapshot": jsonv(`{}`),
		"submit_executor_session": "session-1", "submit_dispatch_state": "prepared",
		"submit_effect_class": "unknown",
		"submit_prepared_at":  now,
	}
}

func publicationSlot(permit, root string, now time.Time) map[string]any {
	return map[string]any{
		"publication_permit_id": permit, "publication_attempt": 1,
		"publication_work_item_id": "work-" + permit, "publication_fencing_token": 1,
		"publication_owner_version": 1, "publication_effect_key": "key-" + permit,
		"publication_spec_digest": acqDigest("a"), "publication_plan_digest": acqDigest("b"),
		"publication_dependency_digest": acqDigest("c"),
		"publication_frozen_objects":    jsonv(`{}`), "publication_step_checkpoint": jsonv(`{}`),
		"publication_step_proof_bytes": jsonv(`{}`), "publication_executor_session": "session-1",
		"publication_dispatch_state": "prepared", "publication_effect_class": "unknown",
		"publication_reconcile_wall_seconds": 3600, "publication_max_lookups": 5,
		"publication_prepared_at": now,
	}
}

// insertSubmissionSlot seeds a root (and its task) and binds one slot.
func insertSubmissionSlot(t *testing.T, env *rootEnv, permit string, overrides map[string]any) error {
	t.Helper()
	root := env.root(t, validInput)
	return applySlot(env.db, submissionTable, "id", rootEnvTaskID(root),
		merged(submissionSlot(permit, root, env.now), overrides))
}

// insertPublicationSlot seeds an operation owner row and binds one slot. The
// fixture uses the completed-operation helper so the artifact relation and the
// required-file requirement are the same ones production prepares against.
func insertPublicationSlot(t *testing.T, env *rootEnv, permit, tag string, overrides map[string]any) error {
	t.Helper()
	operation := "op-" + tag
	seedCompletedFileOperation(t, env.db, tag, operation)
	return applySlot(env.db, publicationTable, "id", operation,
		merged(publicationSlot(permit, tag, env.now), overrides))
}

// attemptCases are the dispatch, time and proof matrix both slot shapes share.
// `p` is the owner prefix (`submit_` or `publication_`). start is a time after
// prepared_at, used wherever a dispatch has begun.
func attemptCases(now time.Time, p string, wall time.Duration, maxLookups int) []struct {
	name, want string
	set        map[string]any
} {
	start := now.Add(time.Second)
	closed := map[string]any{p + "dispatch_state": "closed", p + "dispatch_started_at": start, p + "closed_proof": jsonv(`{"kind":"channel_closed"}`)}
	return []struct {
		name, want string
		set        map[string]any
	}{
		{"a prepared attempt", "", nil},
		{"a dispatching attempt", "", map[string]any{p + "dispatch_state": "dispatching", p + "dispatch_started_at": start}},
		{"a closed attempt that dispatched", "", merged(closed, map[string]any{p + "effect_class": "confirmed"})},
		{"a closed attempt that never dispatched", "", map[string]any{p + "dispatch_state": "closed", p + "closed_proof": jsonv(`{"kind":"no_dispatch"}`), p + "effect_class": "no_external_effect"}},
		{"an unknown attempt with its reconciliation window", "", map[string]any{
			p + "dispatch_state": "dispatching", p + "dispatch_started_at": start, p + "unknown_since": start, p + "reconcile_until": start.Add(wall)}},

		{"a closed attempt that never dispatched and does not say so", "23514", map[string]any{p + "dispatch_state": "closed", p + "closed_proof": jsonv(`{"kind":"channel_closed"}`)}},
		{"a prepared attempt that has a start time", "23514", map[string]any{p + "dispatch_started_at": start}},
		{"a prepared attempt that has a closed proof", "23514", map[string]any{p + "closed_proof": jsonv(`{"k":1}`)}},
		{"a dispatching attempt with no start time", "23514", map[string]any{p + "dispatch_state": "dispatching"}},
		{"a dispatching attempt that already has a closed proof", "23514", map[string]any{
			p + "dispatch_state": "dispatching", p + "dispatch_started_at": start, p + "closed_proof": jsonv(`{"k":1}`)}},
		{"a closed attempt with no closed proof", "23514", map[string]any{p + "dispatch_state": "closed", p + "dispatch_started_at": start}},
		{"a prepared attempt that is not unknown", "23514", map[string]any{p + "effect_class": "confirmed"}},
		{"a dispatch that starts before the attempt was prepared", "23514", map[string]any{
			p + "dispatch_state": "dispatching", p + "dispatch_started_at": now.Add(-time.Second)}},
		{"an unknown time without a reconciliation deadline", "23514", map[string]any{p + "unknown_since": start}},
		{"a reconciliation deadline without an unknown time", "23514", map[string]any{p + "reconcile_until": start}},
		{"a reconciliation deadline that is not unknown_since plus the wall", "23514", map[string]any{
			p + "unknown_since": start, p + "reconcile_until": start.Add(wall + time.Second)}},
		{"more lookups than the budget", "23514", map[string]any{p + "lookup_count": maxLookups + 1}},
		{"a zero fencing token", "23514", map[string]any{p + "fencing_token": 0}},
		{"an empty object standing in for a proof", "23514", merged(closed, map[string]any{p + "closed_proof": jsonv(`{}`)})},
		{"a proof that is not an object", "23514", merged(closed, map[string]any{p + "resolution_proof": jsonv(`[1]`)})},
		{"a conflict that is an empty object", "23514", map[string]any{p + "conflict_evidence": jsonv(`{}`)}},
		{"a bad spec digest", "23514", map[string]any{p + "spec_digest": "sha256:xyz"}},
		{"an empty executor session", "23514", map[string]any{p + "executor_session": ""}},
		{"an effect key over 512 characters", "23514", map[string]any{p + "effect_key": strings.Repeat("k", 513)}},
		{"an unknown effect class", "23514", map[string]any{p + "effect_class": "maybe"}},
		{"an unknown dispatch state", "23514", map[string]any{p + "dispatch_state": "running"}},
	}
}

func TestAcquisitionSubmissionSlotMatrix(t *testing.T) {
	env := newRootEnv(t)
	// The reconcile budget is derived from the root's frozen input limits when the
	// task row is inserted; read it so the window cases match the real slot.
	probe := env.root(t, validInput)
	var wallSeconds, maxLookups int
	if err := env.db.QueryRow(`SELECT submit_reconcile_wall_seconds,submit_max_lookups FROM download_tasks WHERE id=$1`, rootEnvTaskID(probe)).Scan(&wallSeconds, &maxLookups); err != nil {
		t.Fatal(err)
	}
	for i, c := range attemptCases(env.now, "submit_", time.Duration(wallSeconds)*time.Second, maxLookups) {
		permit := fmt.Sprintf("sub-%d", i)
		expectState(t, "submission: "+c.name, insertSubmissionSlot(t, env, permit, c.set), c.want)
	}

	for i, bad := range []struct {
		name string
		set  map[string]any
	}{
		{"a path snapshot that is not an object", map[string]any{"submit_path_snapshot": jsonv(`"p"`)}},
		{"a lookup ticket that is an empty object", map[string]any{"submit_lookup_ticket": jsonv(`{}`)}},
	} {
		permit := fmt.Sprintf("sub-bad-%d", i)
		expectState(t, "submission: "+bad.name, insertSubmissionSlot(t, env, permit, bad.set), "23514")
	}

	// A resolved attempt does not block a new one on the same key.
	safe := map[string]any{
		"submit_dispatch_state": "closed", "submit_dispatch_started_at": env.now.Add(time.Second),
		"submit_closed_proof": jsonv(`{"kind":"channel_closed"}`),
		"submit_effect_class": "no_external_effect", "submit_resolution_proof": jsonv(`{"k":1}`), "submit_retry_safe": true}
	expectState(t, "a retry-safe submission", insertSubmissionSlot(t, env, "sub-safe", safe), "")
	notSafe := merged(safe, map[string]any{"submit_effect_class": "confirmed"})
	expectState(t, "a retry-safe submission whose effect was confirmed", insertSubmissionSlot(t, env, "sub-bad-safe", notSafe), "23514")
	noProof := merged(safe, map[string]any{"submit_resolution_proof": nil})
	expectState(t, "a retry-safe submission with no resolution proof", insertSubmissionSlot(t, env, "sub-no-proof", noProof), "23514")
}

func TestAcquisitionPublicationSlotMatrix(t *testing.T) {
	env := newRootEnv(t)
	for i, c := range attemptCases(env.now, "publication_", 3600*time.Second, 5) {
		permit := fmt.Sprintf("pub-%d", i)
		expectState(t, "publication: "+c.name, insertPublicationSlot(t, env, permit, fmt.Sprintf("pm%d", i), c.set), c.want)
	}

	for i, bad := range []struct {
		name string
		set  map[string]any
	}{
		{"an unknown operation", map[string]any{"operation": "rename"}},
		{"an unknown intent", map[string]any{"intent": "force"}},
		{"an unknown collision policy", map[string]any{"collision_policy": "merge"}},
		{"an attempt ordinal over 100", map[string]any{"publication_attempt": 101}},
		{"a bad plan digest", map[string]any{"publication_plan_digest": "nope"}},
		{"a step checkpoint that is not an object", map[string]any{"publication_step_checkpoint": jsonv(`[]`)}},
		{"frozen objects that are not an object", map[string]any{"publication_frozen_objects": jsonv(`[]`)}},
	} {
		permit := fmt.Sprintf("pub-bad-%d", i)
		expectState(t, "publication: "+bad.name, insertPublicationSlot(t, env, permit, fmt.Sprintf("pb%d", i), bad.set), "23514")
	}

	// retry_safe here needs a closed attempt with a decided effect; unlike the
	// download client it does not demand "no external effect", so a confirmed
	// prefix can be continued.
	closed := map[string]any{"publication_dispatch_state": "closed", "publication_dispatch_started_at": env.now.Add(time.Second),
		"publication_closed_proof": jsonv(`{"kind":"channel_closed"}`)}
	expectState(t, "a retry-safe confirmed prefix",
		insertPublicationSlot(t, env, "pub-prefix", "pp0", merged(closed, map[string]any{"publication_effect_class": "confirmed", "publication_retry_safe": true})), "")
	expectState(t, "a retry-safe attempt whose effect is still unknown",
		insertPublicationSlot(t, env, "pub-undecided", "pp1", merged(closed, map[string]any{"publication_effect_class": "unknown", "publication_retry_safe": true})), "23514")
	expectState(t, "a retry-safe attempt that is not closed",
		insertPublicationSlot(t, env, "pub-not-closed", "pp2", map[string]any{"publication_retry_safe": true}), "23514")
}

// The owner rows must not reintroduce a cascading delete path into the
// acquisition graph, and the runtime role may only write the ordinary owner
// columns — never the submission/publication proof columns.
func TestEffectSlotsNeverCascadeAndRuntimeCannotWriteProofColumns(t *testing.T) {
	env := newRootEnv(t)
	ctx := context.Background()
	for _, table := range []string{submissionTable, publicationTable} {
		var cascading int
		if err := env.db.QueryRowContext(ctx, `
SELECT count(*) FROM pg_constraint
WHERE conrelid = $1::regclass AND contype = 'f' AND confdeltype <> 'a'`, table).Scan(&cascading); err != nil {
			t.Fatal(err)
		}
		if cascading != 0 {
			t.Errorf("%s has %d foreign keys with a delete action other than no action", table, cascading)
		}
		rows, err := env.db.QueryContext(ctx, `
SELECT p, has_table_privilege('bansoko_runtime', $1, p)
FROM unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER']) AS p`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var privilege string
			var granted bool
			if err := rows.Scan(&privilege, &granted); err != nil {
				t.Fatal(err)
			}
			want := privilege == "SELECT" || privilege == "DELETE"
			if granted != want {
				t.Errorf("runtime privilege %s on %s = %t, want %t", privilege, table, granted, want)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}

	root := env.root(t, validInput)
	if err := applySlot(env.db, submissionTable, "id", rootEnvTaskID(root),
		submissionSlot("sub-priv", root, env.now)); err != nil {
		t.Fatalf("seed one slot as the owner: %v", err)
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, env.db, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatalf("provision runtime role: %v", err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	for _, statement := range []string{
		`UPDATE download_tasks SET submit_dispatch_state='closed' WHERE false`,
		`UPDATE download_tasks SET submit_retry_safe=true WHERE false`,
		`UPDATE file_operations SET publication_permit_id='forged' WHERE false`,
		`UPDATE file_operations SET publication_closed_proof='{"kind":"forged"}'::jsonb WHERE false`,
	} {
		if _, err := runtime.ExecContext(ctx, statement); err == nil {
			t.Errorf("the runtime role must be denied: %s", statement)
		} else if !isInsufficientPrivilege(err) {
			t.Errorf("runtime statement %q: error = %v, want insufficient_privilege", statement, err)
		}
	}
	// The ordinary owner columns keep their pre-fold direct write privilege.
	if _, err := runtime.ExecContext(ctx, `UPDATE download_tasks SET state='queued' WHERE false`); err != nil && isInsufficientPrivilege(err) {
		t.Errorf("ordinary owner column write lost its privilege: %v", err)
	}
}
