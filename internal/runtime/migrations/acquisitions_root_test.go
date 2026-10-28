package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/runtime/database"
)

// These tests prove the shape and the privileges of the independent acquisition
// root table: what a row may and may not look like, that a request key is unique
// per scope and namespace, and that the runtime role can read a root but never
// write one directly.

type acqColumn struct{ name, cast string }

var acqColumns = []acqColumn{
	{"id", "text"}, {"execution_branch", "text"}, {"entry_kind", "text"}, {"access_scope", "text"},
	{"request_namespace", "text"}, {"request_key", "text"}, {"request_hash", "text"}, {"canonical_request", "jsonb"},
	{"admission_kind", "text"}, {"admission_http_status", "integer"}, {"admission_receipt", "jsonb"},
	{"input_schema_version", "integer"}, {"admission_digest", "text"}, {"display_title", "jsonb"},
	{"accepted_at", "timestamptz"}, {"retention_anchor_at", "timestamptz"}, {"detail_retention_seconds", "integer"},
	{"receipt_after_accept_seconds", "integer"}, {"receipt_after_terminal_seconds", "integer"}, {"receipt_until", "timestamptz"},
	{"detail_until", "timestamptz"}, {"state", "text"}, {"terminal_at", "timestamptz"}, {"terminal_schema", "text"},
	{"terminal_receipt", "jsonb"}, {"record_state", "text"},
	{"preparation_mode", "text"}, {"preparation_digest", "text"}, {"facts_revision", "bigint"}, {"checked_revision", "bigint"},
	{"settlement_error_count", "integer"},
	{"input_canonical_utf8", "text"}, {"payload_digest", "text"},
	{"manifest_digest", "text"}, {"manifest_item_count", "integer"}, {"manifest_sealed_at", "timestamptz"},
	{"preparation_witness_canonical_utf8", "text"},
	{"requirements_manifest_digest", "text"}, {"requirements_selection_digest", "text"}, {"requirements_evidence_digest", "text"},
	{"requirements_digest", "text"}, {"requirements_item_count", "integer"}, {"requirements_required_count", "integer"},
	{"requirements_sealed_at", "timestamptz"},
	{"target_requirements_digest", "text"}, {"target_digest", "text"}, {"target_sealed_at", "timestamptz"},
	{"vars_evaluation_canonical_utf8", "text"},
}

func acqDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

const thirtyDays = 30 * 24 * 3600

// validAcq is a complete, valid running download root.
func validAcq(id, key string, accepted time.Time) map[string]any {
	return map[string]any{
		"id": id, "execution_branch": "download", "entry_kind": "one_shot", "access_scope": "deployment_owner_v1",
		"request_namespace": "manual_acquire_v1", "request_key": key, "request_hash": acqDigest("a"),
		"canonical_request": `{"a":1}`, "admission_kind": "download_queued", "admission_http_status": 202,
		"admission_receipt": `{}`, "input_schema_version": 1, "admission_digest": domainDigest("acquisition-input:v1", `{"schema_version":1}`),
		"display_title": `{"state":"known","value":"t","reason_code":null}`,
		"accepted_at":   accepted, "retention_anchor_at": accepted,
		"detail_retention_seconds": thirtyDays, "receipt_after_accept_seconds": thirtyDays, "receipt_after_terminal_seconds": 2 * thirtyDays,
		"receipt_until": accepted.Add(thirtyDays * time.Second), "detail_until": nil,
		"state": "running", "terminal_at": nil, "terminal_schema": nil, "terminal_receipt": nil,
		"record_state":     "full",
		"preparation_mode": "none", "preparation_digest": nil, "facts_revision": 0, "checked_revision": 0,
		"settlement_error_count": 0,
		"input_canonical_utf8":   `{"schema_version":1}`, "payload_digest": acqDigest("c"),
		"manifest_digest": acqDigest("8"), "manifest_item_count": 0, "manifest_sealed_at": accepted,
		"preparation_witness_canonical_utf8": nil,
		"requirements_manifest_digest":       nil,
		"requirements_selection_digest":      nil,
		"requirements_evidence_digest":       nil,
		"requirements_digest":                nil,
		"requirements_item_count":            nil,
		"requirements_required_count":        nil,
		"requirements_sealed_at":             nil,
		"target_requirements_digest":         nil,
		"target_digest":                      nil,
		"target_sealed_at":                   nil,
		"vars_evaluation_canonical_utf8":     nil,
	}
}

func with2(row map[string]any, overrides map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAcq(db execer, row map[string]any) error {
	names := make([]string, 0, len(acqColumns))
	placeholders := make([]string, 0, len(acqColumns))
	args := make([]any, 0, len(acqColumns))
	columns := acqColumns
	// The compaction columns are written only when the row names them.
	for _, c := range []acqColumn{{"compacted_at", "timestamptz"}, {"compact_trigger", "text"}} {
		if _, ok := row[c.name]; ok {
			columns = append(columns[:len(columns):len(columns)], c)
		}
	}
	for i, c := range columns {
		names = append(names, c.name)
		placeholders = append(placeholders, fmt.Sprintf("$%d::%s", i+1, c.cast))
		args = append(args, row[c.name])
	}
	_, err := db.ExecContext(context.Background(),
		"INSERT INTO acquisitions("+strings.Join(names, ",")+") VALUES ("+strings.Join(placeholders, ",")+")", args...)
	return err
}

// seedQueuedRoot commits one queued-branch root together with the graph the
// deferred accept guard demands: the real release
// chain, the unique rooted download task and the two first work items
// (qb_submit on the task, acquisition_settle on the root). Everything shares
// one transaction because the guard re-reads the graph at commit. The row's
// admission receipt is rewritten to name the task, as the guard requires the
// receipt/task correspondence.
func seedQueuedRoot(t *testing.T, db *sql.DB, row map[string]any) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	seedQueuedRootTx(t, tx, row)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit queued root seed: %v", err)
	}
}

// seedQueuedRootTx is the transaction-scoped body of seedQueuedRoot, so tests
// that add more writes (or provoke the deferred guards) share the commit.
func seedQueuedRootTx(t *testing.T, tx *sql.Tx, row map[string]any) {
	t.Helper()
	id := row["id"].(string)
	tag := "q-" + id
	inputCanonical := validQueued(t, id, "fixture-input-"+id, tag).InputCanonical
	row["admission_digest"] = domainDigest("acquisition-input:v1", inputCanonical)
	row["input_schema_version"] = 1
	row["input_canonical_utf8"] = inputCanonical
	row["manifest_digest"] = acqDigest("8")
	row["manifest_item_count"] = 2
	row["manifest_sealed_at"] = row["accepted_at"]
	row["admission_receipt"] = fmt.Sprintf(
		`{"schema_version":1,"acquisition_id":%q,"admission_kind":"download_queued","download_task_id":%q,"admission_digest":%q}`,
		id, "dt-"+tag, row["admission_digest"])
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-` + tag + `', 'Show')`,
		`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-` + tag + `', 'anime-` + tag + `', 'custom', 'Show', 'active')`,
		`
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-` + tag + `', 'sub-` + tag + `', 'custom_rss', 'https://feeds.example/` + tag + `', 'https://feeds.example/` + tag + `', 'G', 'Show')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('src-` + tag + `')`,
		`
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-` + tag + `', 'src-` + tag + `', 'key', '{}', '{}', 'discovered', 'new')`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed owner chain: %v", err)
		}
	}
	if err := insertAcq(tx, row); err != nil {
		t.Fatalf("seed root row: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO download_tasks(id, release_id, acquisition_id, frozen_input, frozen_policy, state, idempotency_key)
VALUES ('dt-`+tag+`', 'rel-`+tag+`', $1, '{}', '{}', 'queued', 'dt-`+tag+`')`, id); err != nil {
		t.Fatalf("seed download task: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE releases SET download_task_id = 'dt-`+tag+`', verdict = 'submitted' WHERE id = 'rel-`+tag+`'`); err != nil {
		t.Fatalf("link release to task: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-sub-`+tag+`', 'download_task', 'dt-`+tag+`', $1, 'qb_submit', 'dt-`+tag+`', 3, '{}', 1)`, id); err != nil {
		t.Fatalf("seed submit work item: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-set-`+tag+`', 'acquisition', $1, $1, 'acquisition_settle', 'wi-set-`+tag+`', 3, '{"schema_version":1}', 1)`, id); err != nil {
		t.Fatalf("seed settle work item: %v", err)
	}
}

// acquisitionOwnedRelation reports whether a relation may reference the
// acquisition root: the acquisition_* children and the execution graph tables
// that carry an acquisition root column.
func acquisitionOwnedRelation(name string) bool {
	switch name {
	case "download_tasks", "artifacts", "artifact_files",
		"file_operations", "work_items", "postprocessing_plans":
		return true
	case "download_completions":
		// What happened to the root's qB task. Like the receipts it stays
		// with the root when the execution graph is compacted (it freezes its own
		// copy of the task identity), and a root is never deleted.
		return true
	case "replacement_decisions":
		// stale_acquisition_id names the candidate root whose failure or
		// cancellation staled the decision. The decision belongs to its anime
		// and is deleted with it; the root it names is never deleted.
		return true
	}
	return strings.HasPrefix(name, "acquisition_")
}

func sqlState(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func TestAcquisitionsRootShape(t *testing.T) {
	db := installedDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	n := 0
	next := func() (string, string) { n++; return fmt.Sprintf("acq-%d", n), fmt.Sprintf("key-%d", n) }

	// The deferred accept guard re-reads every new root's graph at commit, so a
	// queued root is only accepted with its task
	// and first work items; positive cases seed the whole graph.
	accept := func(name string, overrides map[string]any) {
		t.Helper()
		id, key := next()
		seedQueuedRoot(t, db, with2(validAcq(id, key, now), overrides))
	}
	reject := func(name string, overrides map[string]any) {
		t.Helper()
		id, key := next()
		err := insertAcq(db, with2(validAcq(id, key, now), overrides))
		if err == nil {
			t.Errorf("%s must be rejected", name)
			return
		}
		if code := sqlState(err); code != "23514" {
			t.Errorf("%s: SQLSTATE = %q (%v), want 23514 check_violation", name, code, err)
		}
	}
	terminal := func(state string) map[string]any {
		return map[string]any{
			"state": state, "terminal_at": now.Add(time.Hour), "terminal_schema": "native_terminal_v1", "terminal_receipt": `{}`,
			"detail_until": now.Add(time.Hour + thirtyDays*time.Second), "receipt_until": now.Add(time.Hour + 2*thirtyDays*time.Second),
		}
	}

	accept("a valid running download root", nil)
	// The adopted branch accepts through its own typed entry, which writes the
	// whole adopted subgraph the guard requires.
	accept("an automatic acquisition in its own namespace", map[string]any{"entry_kind": "automatic", "request_namespace": "auto_acquire_v1"})
	accept("a subscription add", map[string]any{"entry_kind": "subscription_add"})
	accept("a succeeded root with its terminal fields", terminal("succeeded"))
	accept("a failed root with its terminal fields", terminal("failed"))
	accept("a provider-selection preparation with its digest", map[string]any{"preparation_mode": "provider_selection_v1", "preparation_digest": acqDigest("c")})
	// 189: a compact root records when and why it was compacted.
	accept("a compact terminal root", with2(terminal("succeeded"), map[string]any{"record_state": "compact", "compacted_at": now.Add(2 * time.Hour), "compact_trigger": "detail_expired"}))
	accept("a cancelled root with its terminal fields", terminal("cancelled"))

	reject("a download branch with the adopted admission", map[string]any{"admission_kind": "adopted_existing", "admission_http_status": 200})
	reject("an adopted branch with a 202", map[string]any{"execution_branch": "adopted", "admission_kind": "adopted_existing"})
	reject("an unknown branch", map[string]any{"execution_branch": "evidence_only"})
	reject("an automatic acquisition in the manual namespace", map[string]any{"entry_kind": "automatic"})
	reject("a one-shot in the automatic namespace", map[string]any{"request_namespace": "auto_acquire_v1"})
	reject("a retention anchor that is not the acceptance time", map[string]any{"retention_anchor_at": now.Add(time.Second)})
	reject("a bad request hash", map[string]any{"request_hash": "sha256:ABC"})
	reject("a bad admission digest", map[string]any{"admission_digest": strings.Repeat("a", 64)})
	reject("a request that is not an object", map[string]any{"canonical_request": `[1]`})
	reject("a request over 64 KiB", map[string]any{"canonical_request": `{"x":"` + strings.Repeat("y", 70000) + `"}`})
	reject("input schema version 2", map[string]any{"input_schema_version": 2})
	reject("a display title that is not an object", map[string]any{"display_title": `"t"`})
	reject("a running root with a terminal_at", map[string]any{"terminal_at": now.Add(time.Hour)})
	reject("a terminal root with no terminal fields", map[string]any{"state": "succeeded"})
	reject("a terminal root ending before it was accepted", with2(terminal("failed"), map[string]any{"terminal_at": now.Add(-time.Hour)}))
	reject("a terminal root whose detail window is wrong", with2(terminal("failed"), map[string]any{"detail_until": now.Add(time.Hour)}))
	reject("a receipt window shorter than the accepted-receipt promise", map[string]any{"receipt_until": now.Add(time.Hour)})
	reject("a terminal receipt window shorter than its promise", with2(terminal("succeeded"), map[string]any{"receipt_until": now.Add(2 * time.Hour)}))
	reject("a compact running root", map[string]any{"record_state": "compact"})
	reject("a compact root without its compact record", with2(terminal("succeeded"), map[string]any{"record_state": "compact"}))
	reject("a full root with a compact record", with2(terminal("succeeded"), map[string]any{"compacted_at": now.Add(2 * time.Hour), "compact_trigger": "owner_cleanup"}))
	reject("an unknown compact trigger", with2(terminal("succeeded"), map[string]any{"record_state": "compact", "compacted_at": now.Add(2 * time.Hour), "compact_trigger": "oneshot_reclaim"}))
	reject("a preparation digest on preparation none", map[string]any{"preparation_digest": acqDigest("c")})
	reject("a provider-selection preparation without a digest", map[string]any{"preparation_mode": "provider_selection_v1"})
	reject("a checked revision ahead of the facts revision", map[string]any{"checked_revision": 3})
	reject("a settlement error count over 5", map[string]any{"settlement_error_count": 6})
	reject("a zero retention window", map[string]any{"detail_retention_seconds": 0})

	// A request key is one acquisition inside its scope and namespace.
	id, key := next()
	seedQueuedRoot(t, db, validAcq(id, key, now))
	other, _ := next()
	if err := insertAcq(db, validAcq(other, key, now)); sqlState(err) != "23505" {
		t.Errorf("a reused request key must be a unique violation, got %v", err)
	}
	// ... but another namespace or another scope may reuse the same key text.
	other, _ = next()
	seedQueuedRoot(t, db, with2(validAcq(other, key, now), map[string]any{"entry_kind": "automatic", "request_namespace": "auto_acquire_v1"}))
	other, _ = next()
	seedQueuedRoot(t, db, with2(validAcq(other, key, now), map[string]any{"access_scope": "another_scope_v1"}))
	// The id is the primary key.
	if err := insertAcq(db, validAcq(id, "fresh-key", now)); sqlState(err) != "23505" {
		t.Errorf("a reused id must be a unique violation, got %v", err)
	}
}

// The root has no foreign key into the Release/DownloadTask graph or any other
// table: an acquisition must outlive them all. Only acquisition-owned relations
// and the root columns of the execution graph reference it, and none of them
// cascades.
func TestAcquisitionsRootHasNoParents(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	var parents int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM pg_constraint
WHERE conrelid = 'acquisitions'::regclass AND contype = 'f'`).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if parents != 0 {
		t.Fatalf("acquisitions has %d foreign keys, want none", parents)
	}
	// Only acquisition-owned relations may reference the root, and none of them
	// cascades: nothing is deleted behind a deleter's back.
	rows, err := db.QueryContext(ctx, `
SELECT conrelid::regclass::text, confdeltype
FROM pg_constraint WHERE confrelid = 'acquisitions'::regclass AND contype = 'f'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, onDelete string
		if err := rows.Scan(&table, &onDelete); err != nil {
			t.Fatal(err)
		}
		if !acquisitionOwnedRelation(table) {
			t.Errorf("%s references the root but is not an acquisition-owned relation", table)
		}
		if onDelete != "a" {
			t.Errorf("%s references the root with delete action %q, want no action", table, onDelete)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// The runtime role reads a root but cannot write, truncate or reference it: roots
// are created and settled only by owner-held typed functions (a later slice).
func TestAcquisitionRuntimeRoleCannotWriteTheRoot(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedQueuedRoot(t, owner, validAcq("acq-priv", "key-priv", now))

	// The privileges, read from the catalog: the runtime group role has SELECT and
	// nothing else on the root.
	rows, err := owner.QueryContext(ctx, `
SELECT p, has_table_privilege('bansoko_runtime', 'acquisitions', p)
FROM unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER']) AS p`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var privilege string
		var granted bool
		if err := rows.Scan(&privilege, &granted); err != nil {
			t.Fatal(err)
		}
		if want := privilege == "SELECT"; granted != want {
			t.Errorf("runtime privilege %s on acquisitions = %t, want %t", privilege, granted, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatalf("provision runtime role: %v", err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()

	var visible int
	if err := runtime.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&visible); err != nil {
		t.Fatalf("the runtime role must be able to read roots: %v", err)
	}
	if visible != 1 {
		t.Fatalf("runtime sees %d roots, want 1", visible)
	}
	for _, statement := range []string{
		`UPDATE acquisitions SET state = 'failed' WHERE id = 'acq-priv'`,
		`UPDATE acquisitions SET display_title = display_title WHERE id = 'acq-priv'`,
		`DELETE FROM acquisitions WHERE id = 'acq-priv'`,
		`TRUNCATE acquisitions`,
		`ALTER TABLE acquisitions DISABLE TRIGGER ALL`,
	} {
		if _, err := runtime.ExecContext(ctx, statement); err == nil {
			t.Errorf("the runtime role must be denied: %s", statement)
		} else if !isInsufficientPrivilege(err) {
			t.Errorf("runtime statement %q: error = %v, want insufficient_privilege", statement, err)
		}
	}
	valid := validAcq("acq-forged", "key-forged", now)
	if err := insertAcq(runtime, valid); err == nil {
		t.Error("the runtime role must not be able to insert a root directly")
	} else if !isInsufficientPrivilege(err) {
		t.Errorf("runtime insert: error = %v, want insufficient_privilege", err)
	}
	var stored int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("the root table holds %d rows after the denied writes, want the one seeded row", stored)
	}
}
