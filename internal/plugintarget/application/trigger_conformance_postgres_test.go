package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	deliverypayload "bansoko/internal/plugintarget/payload"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// The trigger vocabulary is one closed set with several consumers. Code used to
// declare it five times (port, application constants, payload allowlist,
// template catalog, service validation), and the schema declares it again in
// three CHECKs plus a JSON validator. Nothing compared those copies, so a
// trigger added on one side shipped as a delivery the other side rejects.
//
// These tests make each copy a checked mirror of the port registry. The SQL side
// is read from the installed catalog, not from the migration source, so a
// rewritten or replaced constraint is still the thing under test.

func registrySet() map[string]bool {
	out := make(map[string]bool, len(plugintargetport.Triggers))
	for _, trigger := range plugintargetport.Triggers {
		out[trigger] = true
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compareSets(t *testing.T, name string, got map[string]bool, want map[string]bool) {
	t.Helper()
	for value := range want {
		if !got[value] {
			t.Errorf("%s is missing registry trigger %q", name, value)
		}
	}
	for value := range got {
		if !want[value] {
			t.Errorf("%s carries trigger %q which the registry does not declare", name, value)
		}
	}
}

// TestTriggerMirrorsAgreeWithTheRegistry compares every Go-side copy as a closed
// set. Order is deliberately not compared: the registry's order is a catalog
// concern, while the mirrors only have to agree on membership.
func TestTriggerMirrorsAgreeWithTheRegistry(t *testing.T) {
	registry := registrySet()

	application := map[string]bool{}
	for _, trigger := range []TriggerKind{
		TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed,
		TriggerReleaseDiscovered, TriggerDownloadFailed, TriggerSubscriptionChanged, TriggerUnknownFilesImported,
		TriggerReplacementDeletionFailed,
	} {
		application[string(trigger)] = true
	}
	compareSets(t, "plugintarget/application constants", application, registry)

	compareSets(t, "plugintarget/payload TriggerKinds", deliverypayload.TriggerKinds, registry)
	for _, trigger := range sortedKeys(registry) {
		if !deliverypayload.TriggerKinds[trigger] {
			t.Errorf("payload codec rejects registry trigger %q", trigger)
		}
	}

	// The template catalog owns each trigger's variables and defaults, so it
	// keeps its own declaration — but it must name the same closed set.
	catalog := map[string]bool{}
	for _, trigger := range notification.Triggers {
		if catalog[trigger] {
			t.Errorf("template catalog lists trigger %q twice", trigger)
		}
		catalog[trigger] = true
	}
	compareSets(t, "templatecatalog/notification.Triggers", catalog, registry)

	// Membership alone is not enough for the catalog: a trigger with no
	// scenario cannot render, and one with no default pair cannot preview.
	for _, trigger := range sortedKeys(registry) {
		if _, ok := notification.ScenarioOf(trigger); !ok {
			t.Errorf("template catalog has no scenario for registry trigger %q", trigger)
		}
		if _, ok := notification.Variables(trigger); !ok {
			t.Errorf("template catalog has no variable catalog for registry trigger %q", trigger)
		}
		title, body := notification.Defaults(trigger)
		if title == "" || body == "" {
			t.Errorf("template catalog has no default templates for registry trigger %q", trigger)
		}
	}
	if _, ok := notification.ScenarioOf("not_a_trigger"); ok {
		t.Error("template catalog accepts an unknown trigger")
	}
	if plugintargetport.IsTrigger("not_a_trigger") {
		t.Error("the port registry accepts an unknown trigger")
	}
}

// triggerMirror is one installed SQL object that restates the trigger
// vocabulary. Every one of them must accept exactly the registry set: a mirror
// narrower than the registry rejects a delivery the producer can legitimately
// emit, and a wider one admits a trigger no code path can handle.
type triggerMirror struct {
	table      string
	constraint string
}

// installedTriggerMirrors finds every CHECK constraint whose definition names at
// least one registry trigger. The list is discovered rather than hard-coded so a
// new trigger-bearing table cannot escape the conformance gate.
func installedTriggerMirrors(t *testing.T, db *sql.DB) []triggerMirror {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
SELECT rel.relname, con.conname, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class rel ON rel.oid = con.conrelid
JOIN pg_namespace ns ON ns.oid = rel.relnamespace
WHERE con.contype = 'c' AND ns.nspname = 'public'
ORDER BY rel.relname, con.conname`)
	if err != nil {
		t.Fatalf("list installed constraints: %v", err)
	}
	// Collect before closing the rows: a second query while this one is open
	// needs a spare connection, which the disposable harness does not promise.
	type candidate struct {
		mirror     triggerMirror
		definition string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.mirror.table, &item.mirror.constraint, &item.definition); err != nil {
			rows.Close()
			t.Fatalf("scan constraint: %v", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close constraint rows: %v", err)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}

	known := registrySet()
	var mirrors []triggerMirror
	for _, item := range candidates {
		for trigger := range known {
			if sqlLiteralPresent(item.definition, trigger) {
				mirrors = append(mirrors, item.mirror)
				break
			}
		}
	}
	return mirrors
}

// sqlLiteralPresent reports whether a rendered constraint definition contains the
// trigger as a complete single-quoted literal. The closing quote is what makes
// it exact: "'download_failed'" never matches inside "'download_failed_extra'".
// Casts such as 'domestic'::text contribute their quoted value, which is what a
// trigger literal looks like in the ANY(ARRAY[...]) form pg_get_constraintdef
// renders for an IN list.
func sqlLiteralPresent(definition, trigger string) bool {
	return strings.Contains(definition, "'"+trigger+"'")
}

// testColumnOf returns the columns one installed CHECK actually constrains,
// straight from the catalog. Guessing "trigger_kind" would silently attach the
// predicate to a differently named column, and PostgreSQL accepts a predicate
// over a column it does not have only because the temp table declares it.
func testColumnOf(t *testing.T, db *sql.DB, table, constraint string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
SELECT att.attname
FROM pg_constraint con
JOIN pg_class rel ON rel.oid = con.conrelid
JOIN unnest(con.conkey) WITH ORDINALITY AS key(attnum, ordinal) ON TRUE
JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = key.attnum
WHERE rel.relname = $1 AND con.conname = $2
ORDER BY key.ordinal`, table, constraint)
	if err != nil {
		t.Fatalf("read constrained columns of %s: %v", constraint, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan constrained column: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constrained columns: %v", err)
	}
	if len(columns) == 0 {
		t.Fatalf("%s constrains no column", constraint)
	}
	return columns
}

// probeTable builds a temp table carrying exactly one installed predicate over
// the columns that predicate constrains, so one row can be submitted to the real
// schema rule without constructing a whole owner chain. The name is short on
// purpose: PostgreSQL truncates identifiers at 63 bytes, and two long names that
// share a prefix would silently collide.
func probeTable(t *testing.T, db *sql.DB, name string, columns []string, predicate string) string {
	t.Helper()
	definition := make([]string, len(columns))
	for i, column := range columns {
		definition[i] = column + " TEXT"
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE TEMP TABLE `+name+` (`+strings.Join(definition, ", ")+`)`); err != nil {
		t.Fatalf("create probe table %s: %v", name, err)
	}
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE `+name+` ADD CONSTRAINT `+name+`_check CHECK (`+predicate+`)`); err != nil {
		t.Fatalf("attach the installed predicate to %s: %v", name, err)
	}
	return name
}

// is a rejection; anything else is a harness problem, not vocabulary evidence.
func insertAccepts(t *testing.T, db *sql.DB, statement string, args ...any) bool {
	t.Helper()
	_, err := db.ExecContext(context.Background(), statement, args...)
	if err == nil {
		return true
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("probe insert failed for a reason other than the CHECK: %v", err)
	}
	return false
}

// TestInstalledTriggerChecksAcceptExactlyTheRegistry proves every installed SQL
// trigger mirror is the registry set plus nothing. The mirrors are discovered
// from the catalog, so a table that starts carrying the vocabulary later is
// covered without editing this test.
func TestInstalledTriggerChecksAcceptExactlyTheRegistry(t *testing.T) {
	db := openTestDB(t)
	registry := registrySet()
	mirrors := installedTriggerMirrors(t, db)
	if len(mirrors) == 0 {
		t.Fatal("no installed constraint names a trigger: the discovery query or the schema is wrong")
	}

	// Three known mirrors must be among those discovered; the names pin the
	// inventory so a renamed or dropped CHECK is visible here rather than
	// silently reducing coverage.
	for _, required := range []triggerMirror{
		{table: "notification_admissions", constraint: "notification_admissions_trigger_kind_check"},
		{table: "plugin_deliveries", constraint: "plugin_deliveries_trigger_kind_check"},
		{table: "idempotency_records", constraint: "idempotency_records_trigger_kind_check"},
	} {
		found := false
		for _, mirror := range mirrors {
			if mirror == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s on %s no longer names any trigger; the driver column must keep its closed CHECK", required.constraint, required.table)
		}
	}

	for _, mirror := range mirrors {
		var predicate, definition string
		if err := db.QueryRowContext(context.Background(), `
SELECT pg_get_expr(conbin, conrelid), pg_get_constraintdef(oid)
FROM pg_constraint WHERE conname = $1 AND conrelid = $2::regclass`,
			mirror.constraint, mirror.table).Scan(&predicate, &definition); err != nil {
			t.Fatalf("read predicate %s: %v", mirror.constraint, err)
		}

		// The literal set is what catches a mirror that is WIDER than the
		// registry. Probing only registry values cannot: a constraint that also
		// accepts "schema_only_trigger" answers every registry value correctly
		// and admits one trigger no code path can produce.
		for _, literal := range constraintLiterals(definition) {
			if !registry[literal] {
				t.Errorf("%s accepts %q, which the registry does not declare", mirror.constraint, literal)
			}
		}
		for trigger := range registry {
			if !sqlLiteralPresent(definition, trigger) {
				t.Errorf("%s omits registry trigger %q, so the producer can emit a value the schema rejects", mirror.constraint, trigger)
			}
		}

		columns := testColumnOf(t, db, mirror.table, mirror.constraint)
		probe := probeTable(t, db, shortIdentifier(fmt.Sprintf("tp_%s", mirror.constraint)), columns, predicate)
		for trigger := range registry {
			if !insertAccepts(t, db, `INSERT INTO `+probe+` (`+columns[0]+`) VALUES ($1)`, trigger) {
				t.Errorf("%s rejects registry trigger %q", mirror.constraint, trigger)
			}
		}
		for _, unknown := range []string{"not_a_trigger", "download_submitted "} {
			if insertAccepts(t, db, `INSERT INTO `+probe+` (`+columns[0]+`) VALUES ($1)`, unknown) {
				t.Errorf("%s accepts %q, which the registry does not declare", mirror.constraint, unknown)
			}
		}
	}
}

// notInClause returns the values inside the first "NOT IN (" list of a rendered
// function body. The trigger validator is exactly that list; the rest of the body
// names JSONB types, which are not vocabulary members.
func notInClause(body string) ([]string, bool) {
	start := strings.Index(body, "NOT IN (")
	if start < 0 {
		return nil, false
	}
	start += len("NOT IN (")
	end := strings.Index(body[start:], ")")
	if end < 0 {
		return nil, false
	}
	return constraintLiterals(body[start : start+end]), true
}

// constraintLiterals returns every non-empty string literal in a rendered SQL
// fragment, deduplicated in first-seen order. pg_get_constraintdef renders an IN
// list as ANY(ARRAY['a'::text,'b'::text]), so the quoted values are exactly the
// accepted members.
func constraintLiterals(definition string) []string {
	matches := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(definition, -1)
	seen := map[string]bool{}
	var values []string
	for _, match := range matches {
		if match[1] == "" || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		values = append(values, match[1])
	}
	return values
}

// shortIdentifier keeps a generated temp-table name inside PostgreSQL's 63-byte
// identifier limit without two names collapsing onto the same truncation.
func shortIdentifier(name string) string {
	const limit = 48 // leaves room for the "_check" constraint suffix
	if len(name) <= limit {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	return name[:limit-9] + "_" + hex.EncodeToString(digest[:4])
}

// TestPluginTargetTriggerArrayValidatorMatchesTheRegistry proves the JSONB
// validator on plugin_target_configs accepts exactly the registry set. This
// mirror is a function rather than an IN list, so it is exercised through its
// own constraint by inserting arrays.
func TestPluginTargetTriggerArrayValidatorMatchesTheRegistry(t *testing.T) {
	db := openTestDB(t)
	registry := registrySet()
	ctx := context.Background()

	var predicate string
	if err := db.QueryRowContext(ctx, `
SELECT pg_get_expr(con.conbin, con.conrelid)
FROM pg_constraint con
JOIN pg_class rel ON rel.oid = con.conrelid
WHERE rel.relname = 'plugin_target_configs' AND con.conname = 'plugin_target_configs_trigger_kinds_check'`).Scan(&predicate); err != nil {
		t.Fatalf("read array validator predicate: %v", err)
	}
	const probe = "trigger_array_probe"
	if _, err := db.ExecContext(ctx, `CREATE TEMP TABLE `+probe+` (`+testColumnOf(t, db, "plugin_target_configs", "plugin_target_configs_trigger_kinds_check")[0]+` JSONB)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+probe+` ADD CONSTRAINT `+probe+`_check CHECK (`+predicate+`)`); err != nil {
		t.Fatalf("attach array validator: %v", err)
	}

	// The array validator is a function, so its accepted set lives in the
	// function body rather than in the constraint text. The body also names the
	// JSONB types it rejects ('array', 'string', ...), which are not triggers, so
	// only the NOT IN list is read. A body whose shape no longer matches is a
	// failure, not a skip: a validator this test cannot read is a validator it
	// cannot vouch for.
	var functionBody string
	if err := db.QueryRowContext(ctx, `
SELECT pg_get_functiondef(p.oid)
FROM pg_proc p
JOIN pg_namespace ns ON ns.oid = p.pronamespace
WHERE p.proname = 'is_valid_plugin_trigger_kinds' AND ns.nspname = 'public'`).Scan(&functionBody); err != nil {
		t.Fatalf("read is_valid_plugin_trigger_kinds: %v", err)
	}
	accepted, ok := notInClause(functionBody)
	if !ok {
		t.Fatalf("is_valid_plugin_trigger_kinds has no NOT IN list to compare:\n%s", functionBody)
	}
	for _, literal := range accepted {
		if !registry[literal] {
			t.Errorf("is_valid_plugin_trigger_kinds accepts %q, which the registry does not declare", literal)
		}
	}
	for trigger := range registry {
		found := false
		for _, literal := range accepted {
			if literal == trigger {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("is_valid_plugin_trigger_kinds omits registry trigger %q", trigger)
		}
	}

	for trigger := range registry {
		if !insertAccepts(t, db, `INSERT INTO trigger_array_probe (trigger_kinds) VALUES (to_jsonb(ARRAY[$1]::text[]))`, trigger) {
			t.Errorf("the trigger array validator rejects registry trigger %q", trigger)
		}
	}
	// The whole registry at once, since a delivery target may subscribe to all.
	all := make([]string, 0, len(registry))
	for trigger := range registry {
		all = append(all, trigger)
	}
	sort.Strings(all)
	if !insertAccepts(t, db, `INSERT INTO trigger_array_probe (trigger_kinds) VALUES (to_jsonb($1::text[]))`, pq.Array(all)) {
		t.Error("the trigger array validator rejects the complete registry")
	}

	for name, statement := range map[string]string{
		"unknown trigger":      `INSERT INTO trigger_array_probe (trigger_kinds) VALUES ('["not_a_trigger"]'::jsonb)`,
		"duplicate trigger":    `INSERT INTO trigger_array_probe (trigger_kinds) VALUES ('["download_failed","download_failed"]'::jsonb)`,
		"non-string element":   `INSERT INTO trigger_array_probe (trigger_kinds) VALUES ('[1]'::jsonb)`,
		"case-mismatched name": `INSERT INTO trigger_array_probe (trigger_kinds) VALUES ('["Download_Failed"]'::jsonb)`,
	} {
		if insertAccepts(t, db, statement) {
			t.Errorf("the trigger array validator accepts a %s", name)
		}
	}
}
