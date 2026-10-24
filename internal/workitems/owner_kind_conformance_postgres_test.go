package workitems

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// These tests close the gap between the Go owner/kind catalogs and the
// constraints actually installed in PostgreSQL. Each catalog carries a comment
// claiming it mirrors a CHECK; a comment cannot fail, so a drift in either
// direction shipped silently until now.
//
// The probe is the constraint, not a copy of it: the installed predicate is
// read with pg_get_expr and re-attached to a temp table with the same columns,
// so the test evaluates the schema this database actually has rather than the
// migration source that was supposed to produce it.

// The installed constraint names these tests read. They are the schema half of
// the catalogs in types.go.
const (
	ownerKindConstraint      = "work_items_owner_kind_check"
	retryOwnerTypeConstraint = "idempotency_records_owner_type_check"
)

// constraintPredicate returns the installed CHECK expression for one constraint
// on one table. pg_get_constraintdef renders the whole "CHECK ((...))" clause,
// which cannot be attached to a second table; pg_get_expr gives the bare
// boolean expression, which can.
func constraintPredicate(t *testing.T, db *sql.DB, table, constraint string) string {
	t.Helper()
	var expression string
	if err := db.QueryRowContext(context.Background(), `
SELECT pg_get_expr(c.conbin, c.conrelid)
FROM pg_constraint c
WHERE c.conname = $1 AND c.conrelid = $2::regclass`, constraint, table).Scan(&expression); err != nil {
		t.Fatalf("read predicate for %s on %s: %v", constraint, table, err)
	}
	return expression
}

// probeTable builds a temp table carrying exactly one installed predicate over
// the named columns, so one value can be submitted to the real schema rule
// without constructing a whole WorkItem owner chain.
func probeTable(t *testing.T, db *sql.DB, name string, columns []string, predicate string) {
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
}

// probeAccepted submits one row and reports whether the installed predicate
// accepted it. A 23514 is a rejection; any other error is a harness failure, not
// evidence about the vocabulary.
func probeAccepted(t *testing.T, db *sql.DB, insert string, args ...any) bool {
	t.Helper()
	_, err := db.ExecContext(context.Background(), insert, args...)
	if err == nil {
		return true
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("probe insert failed for a reason other than the predicate: %v", err)
	}
	return false
}

var quotedLiteral = regexp.MustCompile(`'([^']*)'`)

// quotedLiterals returns every non-empty string literal in a constraint
// expression, sorted and deduplicated. The casts pg_get_expr renders, such as
// '::text', carry no quoted value of their own and contribute nothing.
func quotedLiterals(definition string) []string {
	seen := make(map[string]bool)
	var values []string
	for _, match := range quotedLiteral.FindAllStringSubmatch(definition, -1) {
		value := match[1]
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func pairKey(ownerType, kind string) string { return ownerType + "\x00" + kind }

// goOwnerKindPairs is the complete pair catalog Go owns: every pair the store
// may create, plus the pairs only the SQL functions create.
func goOwnerKindPairs() (map[string]bool, map[string]bool, map[string]bool) {
	pairs := make(map[string]bool)
	ownerTypes := make(map[string]bool)
	kinds := make(map[string]bool)
	add := func(ownerType, kind string) {
		pairs[pairKey(ownerType, kind)] = true
		ownerTypes[ownerType] = true
		kinds[kind] = true
	}
	for ownerType, shape := range ownerKinds {
		for _, kind := range shape.kinds {
			add(ownerType, kind)
		}
	}
	for _, pair := range dbOnlyOwnerKinds {
		add(pair.ownerType, pair.kind)
	}
	return pairs, ownerTypes, kinds
}

func TestWorkItemsOwnerKindCheckMatchesGoCatalog(t *testing.T) {
	db := openTestDB(t)
	predicate := constraintPredicate(t, db, "work_items", ownerKindConstraint)
	probeTable(t, db, "owner_kind_probe", []string{"owner_type", "kind"}, predicate)

	expected, ownerTypes, kinds := goOwnerKindPairs()

	// The candidate universe is every owner type against every kind named
	// anywhere in the Go catalog, so a plausible-but-unlisted pair cannot hide
	// behind "nothing asked about it".
	for ownerType := range ownerTypes {
		for kind := range kinds {
			accepted := probeAccepted(t, db,
				`INSERT INTO owner_kind_probe (owner_type, kind) VALUES ($1, $2)`, ownerType, kind)
			if want := expected[pairKey(ownerType, kind)]; accepted != want {
				if want {
					t.Errorf("pair (%q, %q) is in the Go catalog but the installed CHECK rejects it", ownerType, kind)
				} else {
					t.Errorf("pair (%q, %q) is not in the Go catalog but the installed CHECK accepts it", ownerType, kind)
				}
			}
		}
	}

	// The cross product cannot reach a vocabulary member that shares no pair
	// with anything Go knows. Comparing the literals as sets does.
	for _, literal := range quotedLiterals(predicate) {
		if !ownerTypes[literal] && !kinds[literal] {
			t.Errorf("%s names %q, which is neither a Go owner type nor a Go kind", ownerKindConstraint, literal)
		}
	}
}

// TestManualRetryOwnerTypesAreInsertable proves every owner type the
// manual-retry entry admits can actually write its idempotency record. The
// direction is a subset, not equality: the column also holds local_cleanup
// records whose owner is a managed anime, which manual retry never admits.
func TestManualRetryOwnerTypesAreInsertable(t *testing.T) {
	db := openTestDB(t)
	const constraint = retryOwnerTypeConstraint
	predicate := constraintPredicate(t, db, "idempotency_records", constraint)
	probeTable(t, db, "retry_owner_probe", []string{"owner_type"}, predicate)

	installed := make(map[string]bool)
	for _, literal := range quotedLiterals(predicate) {
		installed[literal] = true
	}

	for ownerType := range manualRetryOwnerTypes {
		if !installed[ownerType] {
			t.Errorf("manual retry admits %q but %s does not accept it: the request key insert would fail after the owner row was already reset", ownerType, constraint)
		}
		if !probeAccepted(t, db, `INSERT INTO retry_owner_probe (owner_type) VALUES ($1)`, ownerType) {
			t.Errorf("%s rejects manual-retry owner type %q", constraint, ownerType)
		}
	}

	// The drift this test was written to catch: postprocessing_plan sat in the
	// Go allowlist while the column rejected it. Assert the rejection so it
	// cannot quietly return on one side only.
	for _, ownerType := range []string{OwnerPostProcessingPlan, "not_an_owner"} {
		if probeAccepted(t, db, `INSERT INTO retry_owner_probe (owner_type) VALUES ($1)`, ownerType) {
			t.Errorf("%s accepts %q, which manual retry does not admit", constraint, ownerType)
		}
	}
}

// TestManualRetryOwnerTypesHaveOwnerLocks proves the allowlist cannot outgrow
// the prelock that has to address an owner row for every admitted type. The
// prelock map documents that it is derived from manualRetryOwnerTypes; a member
// without a lock silently skips contention protection.
func TestManualRetryOwnerTypesHaveOwnerLocks(t *testing.T) {
	for ownerType := range manualRetryOwnerTypes {
		if _, ok := manualRetryOwnerLocks[ownerType]; !ok {
			t.Errorf("manual retry admits %q but manualRetryOwnerLocks has no owner row for it", ownerType)
		}
	}
	for ownerType := range manualRetryOwnerLocks {
		if !manualRetryOwnerTypes[ownerType] {
			t.Errorf("manualRetryOwnerLocks locks %q which manual retry never admits", ownerType)
		}
	}
}

// TestAcquisitionSettleIsDatabaseOnly pins the one deliberate exception to the
// exact-set rule. If Go ever gains a creation path for the settle item, the pair
// must move out of dbOnlyOwnerKinds — otherwise this test keeps claiming the
// pair is unreachable from Go while the store writes it.
func TestAcquisitionSettleIsDatabaseOnly(t *testing.T) {
	if _, ok := ownerKinds[OwnerAcquisition]; ok {
		t.Fatal("OwnerAcquisition is in ownerKinds; the settle pair belongs in dbOnlyOwnerKinds")
	}
	found := false
	for _, pair := range dbOnlyOwnerKinds {
		if pair.ownerType != OwnerAcquisition || pair.kind != KindAcquisitionSettle {
			continue
		}
		found = true
		if err := (OwnerRef{Type: pair.ownerType, ID: "acq-1"}).validate(); err == nil {
			t.Errorf("the Go store would accept the database-only pair (%q, %q) for creation", pair.ownerType, pair.kind)
		}
	}
	if !found {
		t.Fatalf("dbOnlyOwnerKinds does not name (%q, %q)", OwnerAcquisition, KindAcquisitionSettle)
	}
}
